package maddy

import (
	"context"
	"fmt"
	"strings"

	"github.com/emersion/go-message/textproto"
	"github.com/emersion/go-msgauth/authres"
	"github.com/emersion/go-smtp"
	"github.com/foxcpp/maddy/buffer"
	"github.com/foxcpp/maddy/config"
	"github.com/foxcpp/maddy/log"
	"github.com/foxcpp/maddy/module"
)

// Dispatcher is a object that is responsible for selecting delivery targets
// for the message and running necessary checks and modificators.
//
// It implements module.DeliveryTarget.
//
// It is not a "module object" and is intended to be used as part of message
// source (Submission, SMTP, JMAP modules) implementation.
type Dispatcher struct {
	dispatcherCfg
	hostname string

	Log log.Logger
}

type sourceBlock struct {
	checks      CheckGroup
	rejectErr   error
	perRcpt     map[string]*rcptBlock
	defaultRcpt *rcptBlock
}

type rcptBlock struct {
	checks    CheckGroup
	rejectErr error
	targets   []module.DeliveryTarget
}

func NewDispatcher(globals map[string]interface{}, cfg []config.Node) (*Dispatcher, error) {
	parsedCfg, err := parseDispatcherRootCfg(globals, cfg)
	return &Dispatcher{
		dispatcherCfg: parsedCfg,
	}, err
}

func splitAddress(addr string) (mailbox, domain string, err error) {
	parts := strings.Split(addr, "@")
	switch len(parts) {
	case 1:
		if strings.EqualFold(parts[0], "postmaster") {
			return parts[0], "", nil
		}
		return "", "", fmt.Errorf("malformed address")
	case 2:
		if len(parts[0]) == 0 || len(parts[1]) == 0 {
			return "", "", fmt.Errorf("malformed address")
		}
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("malformed address")
	}
}

func (d *Dispatcher) Start(msgMeta *module.MsgMetadata, mailFrom string) (module.Delivery, error) {
	dl := deliveryLogger(d.Log, msgMeta)
	dd := dispatcherDelivery{
		d:               d,
		rcptChecksState: make(map[*rcptBlock]module.CheckState),
		deliveries:      make(map[module.DeliveryTarget]module.Delivery),
		msgMeta:         msgMeta,
		cancelCtx:       context.Background(),
		log:             &dl,
		checkScore:      0,
	}

	globalChecksState, err := d.globalChecks.NewMessage(msgMeta)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			globalChecksState.Close()
		}
	}()
	if err := dd.checkResult(globalChecksState.CheckConnection(dd.cancelCtx)); err != nil {
		return nil, err
	}
	if err := dd.checkResult(globalChecksState.CheckSender(dd.cancelCtx, mailFrom)); err != nil {
		return nil, err
	}
	dd.globalChecksState = globalChecksState

	// TODO: Init global modificators, run RewriteSender.

	// First try to match against complete address.
	sourceBlock, ok := d.perSource[strings.ToLower(mailFrom)]
	if !ok {
		// Then try domain-only.
		_, domain, err := splitAddress(mailFrom)
		if err != nil {
			return nil, &smtp.SMTPError{
				Code:         501,
				EnhancedCode: smtp.EnhancedCode{5, 1, 3},
				Message:      "Invalid sender address: " + err.Error(),
			}
		}

		sourceBlock, ok = d.perSource[strings.ToLower(domain)]
		if !ok {
			// Fallback to the default source block.
			sourceBlock = d.defaultSource
			dd.log.Debugf("sender %s matched by default rule", mailFrom)
		} else {
			dd.log.Debugf("sender %s matched by domain rule '%s'", mailFrom, strings.ToLower(domain))
		}
	} else {
		dd.log.Debugf("sender %s matched by address rule '%s'", mailFrom, strings.ToLower(mailFrom))
	}

	if sourceBlock.rejectErr != nil {
		dd.log.Debugf("sender %s rejected with error: %v", mailFrom, sourceBlock.rejectErr)
		return nil, sourceBlock.rejectErr
	}
	dd.sourceBlock = sourceBlock

	sourceChecksState, err := sourceBlock.checks.NewMessage(msgMeta)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			sourceChecksState.Close()
		}
	}()
	if err := dd.checkResult(sourceChecksState.CheckConnection(dd.cancelCtx)); err != nil {
		return nil, err
	}
	if err := dd.checkResult(sourceChecksState.CheckSender(dd.cancelCtx, mailFrom)); err != nil {
		return nil, err
	}
	dd.sourceChecksState = sourceChecksState

	// TODO: Init per-sender modificators, run RewriteSender.

	dd.sourceAddr = mailFrom

	return &dd, nil
}

type dispatcherDelivery struct {
	d                 *Dispatcher
	globalChecksState module.CheckState
	sourceChecksState module.CheckState
	rcptChecksState   map[*rcptBlock]module.CheckState

	log *log.Logger

	sourceAddr  string
	sourceBlock sourceBlock

	deliveries map[module.DeliveryTarget]module.Delivery
	msgMeta    *module.MsgMetadata
	cancelCtx  context.Context
	checkScore int32
	authRes    []authres.Result
	header     textproto.Header
}

func (dd *dispatcherDelivery) AddRcpt(to string) error {
	// First try to match against complete address.
	rcptBlock, ok := dd.sourceBlock.perRcpt[strings.ToLower(to)]
	if !ok {
		// Then try domain-only.
		_, domain, err := splitAddress(to)
		if err != nil {
			return &smtp.SMTPError{
				Code:         501,
				EnhancedCode: smtp.EnhancedCode{5, 1, 3},
				Message:      "Invalid recipient address: " + err.Error(),
			}
		}

		rcptBlock, ok = dd.sourceBlock.perRcpt[strings.ToLower(domain)]
		if !ok {
			// Fallback to the default source block.
			rcptBlock = dd.sourceBlock.defaultRcpt
			dd.log.Debugf("recipient %s matched by default rule", to)
		} else {
			dd.log.Debugf("recipient %s matched by domain rule '%s'", to, strings.ToLower(domain))
		}
	} else {
		dd.log.Debugf("recipient %s matched by address rule '%s'", to, strings.ToLower(to))
	}

	if rcptBlock.rejectErr != nil {
		dd.log.Debugf("recipient %s rejected: %v", to, rcptBlock.rejectErr)
		return rcptBlock.rejectErr
	}

	var rcptChecksState module.CheckState
	if rcptChecksState, ok = dd.rcptChecksState[rcptBlock]; !ok {
		var err error
		rcptChecksState, err = rcptBlock.checks.NewMessage(dd.msgMeta)
		if err != nil {
			return err
		}

		if err := dd.checkResult(rcptChecksState.CheckConnection(dd.cancelCtx)); err != nil {
			return err
		}
		if err := dd.checkResult(rcptChecksState.CheckSender(dd.cancelCtx, dd.sourceAddr)); err != nil {
			return err
		}
		dd.rcptChecksState[rcptBlock] = rcptChecksState
	}

	if err := dd.checkResult(rcptChecksState.CheckRcpt(dd.cancelCtx, to)); err != nil {
		return err
	}

	// TODO: Init per-rcpt modificators, run RewriteRcpt.

	for _, target := range rcptBlock.targets {
		var delivery module.Delivery
		var err error
		if delivery, ok = dd.deliveries[target]; !ok {
			delivery, err = target.Start(dd.msgMeta, dd.sourceAddr)
			if err != nil {
				dd.log.Debugf("target.Start(%s) failure, target = %s (%s): %v",
					dd.sourceAddr, target.(module.Module).InstanceName(), target.(module.Module).Name(), err)
				return err
			}

			dd.log.Debugf("target.Start(%s) ok, target = %s (%s)",
				dd.sourceAddr, target.(module.Module).InstanceName(), target.(module.Module).Name())

			dd.deliveries[target] = delivery
		}

		if err := delivery.AddRcpt(to); err != nil {
			dd.log.Debugf("delivery.AddRcpt(%s) failure, Delivery object = %T: %v", to, delivery, err)
			return err
		}
		dd.log.Debugf("delivery.AddRcpt(%s) ok, Delivery object = %T", to, delivery)
	}

	return nil
}

func (dd *dispatcherDelivery) Body(header textproto.Header, body buffer.Buffer) error {
	if err := dd.checkResult(dd.globalChecksState.CheckBody(dd.cancelCtx, header, body)); err != nil {
		return err
	}
	if err := dd.checkResult(dd.sourceChecksState.CheckBody(dd.cancelCtx, header, body)); err != nil {
		return err
	}
	for _, rcptChecksState := range dd.rcptChecksState {
		if err := dd.checkResult(rcptChecksState.CheckBody(dd.cancelCtx, header, body)); err != nil {
			return err
		}
	}

	// After results for all checks are checked, authRes will be populated with values
	// we should put into Authentication-Results header.
	if len(dd.authRes) != 0 {
		header.Add("Authentication-Results", authres.Format(dd.d.hostname, dd.authRes))
	}
	for field := dd.header.Fields(); field.Next(); {
		header.Add(field.Key(), field.Value())
	}

	for _, delivery := range dd.deliveries {
		if err := delivery.Body(header, body); err != nil {
			dd.log.Debugf("delivery.Body failure, Delivery object = %T: %v", delivery, err)
			return err
		}
		dd.log.Debugf("delivery.Body ok, Delivery object = %T", delivery)
	}
	return nil
}

func (dd dispatcherDelivery) Commit() error {
	for _, delivery := range dd.deliveries {
		if err := delivery.Commit(); err != nil {
			dd.log.Debugf("delivery.Commit failure, Delivery object = %T: %v", delivery, err)
			// No point in Committing remaining deliveries, everything is broken already.
			return err
		}
		dd.log.Debugf("delivery.Commit ok, Delivery object = %T", delivery)
	}
	return nil
}

func (dd dispatcherDelivery) Abort() error {
	var lastErr error
	for _, delivery := range dd.deliveries {
		if err := delivery.Abort(); err != nil {
			dd.log.Debugf("delivery.Abort failure, Delivery object = %T: %v", delivery, err)
			lastErr = err
			// Continue anyway and try to Abort all remaining delivery objects.
		}
		dd.log.Debugf("delivery.Abort ok, Delivery object = %T", delivery)
	}
	dd.log.Debugf("delivery aborted")
	return lastErr
}

func (dd *dispatcherDelivery) checkResult(checkResult module.CheckResult) error {
	if checkResult.RejectErr != nil {
		return checkResult.RejectErr
	}
	if checkResult.Quarantine {
		dd.log.Printf("quarantined message due to check result")
		dd.msgMeta.Quarantine = true
	}
	dd.checkScore += checkResult.ScoreAdjust
	if dd.d.rejectScore != nil && dd.checkScore >= int32(*dd.d.rejectScore) {
		dd.log.Debugf("score %d >= %d, rejecting", dd.checkScore, *dd.d.rejectScore)
		return &smtp.SMTPError{
			Code:         550,
			EnhancedCode: smtp.EnhancedCode{5, 7, 0},
			Message:      fmt.Sprintf("Message is rejected due to multiple local policy violations (score %d)", dd.checkScore),
		}
	}
	if dd.d.quarantineScore != nil && dd.checkScore >= int32(*dd.d.quarantineScore) {
		if !dd.msgMeta.Quarantine {
			dd.log.Printf("quarantined message due to score %d >= %d", dd.checkScore, *dd.d.quarantineScore)
		}
		dd.msgMeta.Quarantine = true
	}
	if len(checkResult.AuthResult) != 0 {
		dd.authRes = append(dd.authRes, checkResult.AuthResult...)
	}
	for field := checkResult.Header.Fields(); field.Next(); {
		dd.header.Add(field.Key(), field.Value())
	}
	return nil
}
