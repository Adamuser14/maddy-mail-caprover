package maddy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/emersion/maddy/config"
	"github.com/emersion/maddy/log"
	"github.com/emersion/maddy/module"
)

type SMTPPipelineStep interface {
	// Pass applies step's processing logic to the message.
	//
	// If Pass returns non-nil io.Reader - it should contain new message body.
	// If Pass returns false - message processing will be stopped, if err is
	// not nil - it will be returned to message source.
	Pass(ctx *module.DeliveryContext, msg io.Reader) (newBody io.Reader, continueProcessing bool, err error)
}

type checkSourceHostnameStep struct {
	required bool
}

func checkSourceHostnameStepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	required := false
	if len(node.Args) > 1 {
		return nil, errors.New("check_source_hostname: expected 0 or 1 argument")
	}
	if node.Args[0] == "required" {
		required = true
	} else {
		return nil, errors.New("check_source_hostname: unexpected argument")
	}

	return checkSourceHostnameStep{
		required: required,
	}, nil
}

func (s checkSourceHostnameStep) Pass(ctx *module.DeliveryContext, _ io.Reader) (io.Reader, bool, error) {
	tcpAddr, ok := ctx.SrcAddr.(*net.TCPAddr)
	if !ok {
		log.Debugf("check_source_hostname: not TCP/IP source (%v), skipped", ctx.SrcAddr)
		return nil, true, nil
	}

	srcIPs, err := net.LookupIP(ctx.SrcHostname)
	if err != nil {
		ctx.Ctx["src_hostname_check"] = false
		if s.required {
			log.Printf("check_source_hostname: %s does not resolve, FAIL", ctx.SrcHostname)
			return nil, false, errors.New("source hostname does not resolve")
		} else {
			log.Debugf("check_source_hostname: %s does not resolve, OK", ctx.SrcHostname)
			return nil, true, nil
		}
	}

	for _, ip := range srcIPs {
		if tcpAddr.IP.Equal(ip) {
			ctx.Ctx["src_hostname_check"] = true
			log.Debugf("check_source_hostname: A/AAA record found for %s for %s domain, OK", tcpAddr.IP, ctx.SrcHostname)
			return nil, true, nil
		}
	}
	ctx.Ctx["src_hostname_check"] = false
	if s.required {
		log.Printf("check_source_hostname: no A/AAA records found for %s for %s domain, FAIL", tcpAddr.IP, ctx.SrcHostname)
		return nil, false, errors.New("source hostname does not resolve to source address")
	} else {
		log.Debugf("check_source_hostname: no A/AAA records found for %s for %s domain, OK", tcpAddr.IP, ctx.SrcHostname)
		return nil, true, nil
	}
}

type checkSourceMxStep struct {
	required bool
}

func checkSourceMxStepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	required := false
	if len(node.Args) > 1 {
		return nil, errors.New("check_source_mx: expected 0 or 1 argument")
	}
	if node.Args[0] == "required" {
		required = true
	} else {
		return nil, errors.New("check_source_mx: unexpected argument")
	}

	return checkSourceMxStep{
		required: required,
	}, nil
}

func (s checkSourceMxStep) Pass(ctx *module.DeliveryContext, _ io.Reader) (io.Reader, bool, error) {
	parts := strings.Split(ctx.From, "@")
	if len(parts) != 2 {
		return nil, false, errors.New("malformed address")
	}
	domain := parts[1]

	tcpAddr, ok := ctx.SrcAddr.(*net.TCPAddr)
	if !ok {
		log.Debugf("check_source_mx: not TCP/IP source (%v), skipped", ctx.SrcAddr)
		return nil, true, nil
	}

	srcMx, err := net.LookupMX(domain)
	if err != nil {
		ctx.Ctx["src_mx_check"] = false
		if s.required {
			log.Printf("check_source_mx: %s does not resolve, FAIL, delivery ID = %s", domain, ctx.DeliveryID)
			return nil, false, errors.New("could not find MX records for from domain")
		} else {
			log.Debugf("check_source_mx: %s does not resolve, OK", domain)
			return nil, true, nil
		}
	}

	for _, mx := range srcMx {
		if mx.Host == ctx.SrcHostname || mx.Host == tcpAddr.IP.String() {
			log.Debugf("check_source_mx: MX record %s matches %v (%v), OK", mx.Host, ctx.SrcHostname, tcpAddr.IP)
			ctx.Ctx["src_mx_check"] = true
			return nil, true, nil
		}
	}
	ctx.Ctx["src_mx_check"] = false
	if s.required {
		log.Printf("check_source_mx: no matching MX records for %s (%s), FAIL, delivery ID = %s", ctx.SrcHostname, tcpAddr.IP, ctx.DeliveryID)
		return nil, false, errors.New("From domain has no MX record for itself")
	} else {
		log.Debugln("check_source_mx: no MX records, OK")
		return nil, true, nil
	}
}

type checkSourceReverseDNSStep struct {
	required bool
}

func checkSourceReverseDNSStepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	required := false
	if len(node.Args) > 1 {
		return nil, errors.New("check_source_rdns: expected 0 or 1 argument")
	}
	if node.Args[0] == "required" {
		required = true
	} else {
		return nil, errors.New("check_source_rdns: unexpected argument")
	}

	return checkSourceReverseDNSStep{
		required: required,
	}, nil
}

func (s checkSourceReverseDNSStep) Pass(ctx *module.DeliveryContext, _ io.Reader) (io.Reader, bool, error) {
	tcpAddr, ok := ctx.SrcAddr.(*net.TCPAddr)
	if !ok {
		log.Debugf("check_source_rdns: non TCP/IP source (%v), skipped", ctx.SrcAddr)
		return nil, true, nil
	}

	names, err := net.LookupAddr(tcpAddr.IP.String())
	if err != nil || len(names) == 0 {
		ctx.Ctx["src_rdns_check"] = false
		if s.required {
			log.Printf("check_source_rdns: rDNS query for %v failed (%v), FAIL, delivery ID = %s", tcpAddr.IP, err, ctx.DeliveryID)
			return nil, false, errors.New("could look up rDNS address for source")
		} else {
			log.Debugf("check_source_rdns: rDNS query for %v failed (%v), OK", tcpAddr.IP, err)
			return nil, true, nil
		}
	}

	srcDomain := strings.TrimRight(ctx.SrcHostname, ".")

	for _, name := range names {
		if strings.TrimRight(name, ".") == srcDomain {
			ctx.Ctx["src_rdns_check"] = true
			log.Debugf("check_source_rdns: PTR record %s matches source domain, OK", name)
			return nil, true, nil
		}
	}
	ctx.Ctx["src_rdns_check"] = false
	if s.required {
		log.Printf("check_source_rdns: no PTR records for %v IP pointing to %s, FAIL, delivery ID = %s", tcpAddr.IP, srcDomain, ctx.DeliveryID)
		return nil, false, errors.New("rDNS name does not match source hostname")
	} else {
		log.Debugf("check_source_rdns: no PTR records for %v IP pointing to %s, OK", tcpAddr.IP, srcDomain)
		return nil, true, nil
	}
}

type requireAuthStep struct{}

func (requireAuthStep) Pass(ctx *module.DeliveryContext, _ io.Reader) (io.Reader, bool, error) {
	if ctx.AuthUser != "" {
		return nil, true, nil
	}
	return nil, false, &smtp.SMTPError{
		Code:    550,
		Message: "Authentication is required",
	}
}

type filterStep struct {
	f    module.Filter
	opts map[string]string
}

func (step filterStep) Pass(ctx *module.DeliveryContext, msg io.Reader) (io.Reader, bool, error) {
	ctx.Opts = step.opts
	r, err := step.f.Apply(ctx, msg)
	if err == module.ErrSilentDrop {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return r, true, nil
}

type deliverStep struct {
	t    module.DeliveryTarget
	opts map[string]string
}

func LookupAddr(ip net.IP) (string, error) {
	names, err := net.LookupAddr(ip.String())
	if err != nil || len(names) == 0 {
		return "", err
	}
	return strings.TrimRight(names[0], "."), nil
}

func (step deliverStep) Pass(ctx *module.DeliveryContext, msg io.Reader) (io.Reader, bool, error) {
	// We can safetly assume at least one recipient.
	to := sanitizeString(ctx.To[0])

	received := "Received: from " + ctx.SrcHostname
	if tcpAddr, ok := ctx.SrcAddr.(*net.TCPAddr); ok {
		domain, err := LookupAddr(tcpAddr.IP)
		if err != nil {
			received += fmt.Sprintf(" ([%v])", tcpAddr.IP)
		} else {
			received += fmt.Sprintf(" (%s [%v])", domain, tcpAddr.IP)
		}
	}
	received += fmt.Sprintf("\r\n\tby %s (envelope-sender <%s>)", sanitizeString(ctx.OurHostname), sanitizeString(ctx.From))
	received += fmt.Sprintf("\r\n\twith %s id %s", ctx.SrcProto, ctx.DeliveryID)
	received += fmt.Sprintf("\r\n\tfor %s; %s\r\n", to, time.Now().Format(time.RFC1123Z))

	msg = io.MultiReader(strings.NewReader(received), msg)

	ctx.Opts = step.opts
	err := step.t.Deliver(*ctx, msg)
	return nil, err == nil, err
}

type matchStep struct {
	field    string
	pattern  string
	inverted bool
	regexp   *regexp.Regexp
	substeps []SMTPPipelineStep
}

func (step matchStep) Pass(ctx *module.DeliveryContext, msg io.Reader) (io.Reader, bool, error) {
	match := false
	switch step.field {
	case "rcpt":
		for _, rcpt := range ctx.To {
			if step.matches(rcpt) {
				match = true
				break
			}
		}
	case "rcpt_domain":
		for _, rcpt := range ctx.To {
			parts := strings.Split(rcpt, "@")
			if len(parts) != 2 {
				return nil, false, errors.New("invalid address")
			}
			if step.matches(parts[1]) {
				match = true
				break
			}
		}
	case "from":
		match = step.matches(ctx.From)
	case "from_domain":
		parts := strings.Split(ctx.From, "@")
		if len(parts) != 2 {
			return nil, false, errors.New("invalid address")
		}
		match = step.matches(parts[1])
	case "src_addr":
		match = step.matches(ctx.SrcAddr.String())
	case "src_hostname":
		match = step.matches(ctx.SrcHostname)
	}

	if !match {
		return nil, true, nil
	}

	return passThroughPipeline(step.substeps, ctx, msg)
}

func (step matchStep) matches(s string) bool {
	var match bool
	if step.regexp != nil {
		match = step.regexp.MatchString(s)
	} else {
		match = strings.Contains(s, step.pattern)
	}

	if step.inverted {
		return !match
	} else {
		return match
	}
}

type stopStep struct{}

func (stopStep) Pass(_ *module.DeliveryContext, _ io.Reader) (io.Reader, bool, error) {
	return nil, false, nil
}

func filterStepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	if len(node.Args) == 0 {
		return nil, errors.New("filter: expected at least one argument")
	}

	modInst, err := module.GetInstance(node.Args[0])
	if err != nil {
		return nil, fmt.Errorf("filter: unknown module instance: %s", node.Args[0])
	}

	filterInst, ok := modInst.(module.Filter)
	if !ok {
		return nil, fmt.Errorf("filter: module %s (%s) doesn't implements Filter", modInst.Name(), modInst.InstanceName())
	}

	return filterStep{
		f:    filterInst,
		opts: readOpts(node.Args[1:]),
	}, nil
}

func deliverStepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	if len(node.Args) == 0 {
		return nil, errors.New("deliver: expected at least one argument")
	}

	modInst, err := module.GetInstance(node.Args[0])
	if err != nil {
		return nil, fmt.Errorf("deliver: unknown module instance: %s", node.Args[0])
	}

	deliveryInst, ok := modInst.(module.DeliveryTarget)
	if !ok {
		return nil, fmt.Errorf("deliver: module %s (%s) doesn't implements DeliveryTarget", modInst.Name(), modInst.InstanceName())
	}

	return deliverStep{
		t:    deliveryInst,
		opts: readOpts(node.Args[1:]),
	}, nil
}

func readOpts(args []string) (res map[string]string) {
	res = make(map[string]string)
	for _, arg := range args {
		parts := strings.SplitN(arg, "=", 1)
		if len(parts) == 1 {
			res[parts[0]] = ""
		} else {
			res[parts[0]] = parts[1]
		}

	}
	return
}

func matchStepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	if len(node.Args) != 3 && len(node.Args) != 2 {
		return nil, errors.New("match: expected 3 or 2 arguments")
	}

	res := matchStep{}

	if len(node.Args) == 3 {
		if node.Args[0] != "no" {
			return nil, errors.New("expected 'no' after match")
		}
		res.inverted = true
		node.Args = node.Args[1:]
	}

	res.field = node.Args[0]
	res.pattern = node.Args[1]
	if strings.HasPrefix(res.pattern, "/") && strings.HasSuffix(res.pattern, "/") {
		res.pattern = res.pattern[1 : len(res.pattern)-1]
		var err error
		res.regexp, err = regexp.Compile(res.pattern)
		if err != nil {
			return nil, err
		}
	}

	for _, child := range node.Children {
		step, err := StepFromCfg(child)
		if err != nil {
			return nil, err
		}
		res.substeps = append(res.substeps, step)
	}

	return res, nil
}

type destinationCond struct {
	value  string
	regexp *regexp.Regexp
}

func (cond destinationCond) matches(addr string) bool {
	if cond.regexp != nil {
		return cond.regexp.MatchString(addr)
	}

	if strings.Contains(cond.value, "@") {
		return cond.value == addr
	}

	parts := strings.Split(addr, "@")
	if len(parts) != 2 {
		return false
	}

	// Domains are always case-sensetive.
	return strings.ToLower(parts[1]) == strings.ToLower(cond.value)
}

type destinationStep struct {
	conds    []destinationCond
	substeps []SMTPPipelineStep
}

func (step *destinationStep) Pass(ctx *module.DeliveryContext, msg io.Reader) (io.Reader, bool, error) {
	ctxCpy := ctx.DeepCopy()
	unmatchedRcpts := make([]string, 0, len(ctx.To))
	ctxCpy.To = make([]string, 0, len(ctx.To))
	for _, rcpt := range ctx.To {
		for _, cond := range step.conds {
			if cond.matches(rcpt) {
				ctxCpy.To = append(ctxCpy.To, rcpt)
				break
			} else {
				unmatchedRcpts = append(unmatchedRcpts, rcpt)
			}
		}
	}

	if len(ctxCpy.To) == 0 {
		return nil, true, nil
	}

	// Remove matched recipients from original context value, so only one
	// 'destination' block will ever handle particular recipient.
	// Note: we are passing ctxCpy to substeps, not ctx.
	//		 ctxCpy.To contains matched rcpts, ctx.To contains unmatched
	ctx.To = unmatchedRcpts

	newBody, shouldContinue, err := passThroughPipeline(step.substeps, ctxCpy, msg)
	return newBody, shouldContinue && len(ctx.To) != 0, err
}

func passThroughPipeline(steps []SMTPPipelineStep, ctx *module.DeliveryContext, msg io.Reader) (io.Reader, bool, error) {
	buf, ok := msg.(*bytes.Buffer)
	if !ok {
		buf = new(bytes.Buffer)
		if _, err := buf.ReadFrom(msg); err != nil {
			return nil, false, err
		}
	}
	currentMsg := bytes.NewReader(buf.Bytes())

	for _, step := range steps {
		log.Debugf("running %T step...", step)
		r, cont, err := step.Pass(ctx, currentMsg)
		if !cont {
			if err == module.ErrSilentDrop {
				return nil, false, nil
			}
			if err != nil {
				return nil, true, err
			}
		}

		if r != nil && r != io.Reader(currentMsg) {
			buf.Reset()
			if _, err := io.Copy(buf, r); err != nil {
				log.Debugf("msg rebuffering failed: %v", err)
				return nil, false, err
			}
			currentMsg.Reset(buf.Bytes())
		}
		currentMsg.Seek(0, io.SeekStart)
	}

	if currentMsg != msg {
		return currentMsg, true, nil
	}
	return msg, true, nil
}

func destinationStepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	if len(node.Args) == 0 {
		return nil, errors.New("required at least one condition")
	}

	step := destinationStep{}

	for _, arg := range node.Args {
		if strings.HasPrefix(arg, "/") && strings.HasSuffix(arg, "/") {
			re, err := regexp.Compile(arg[1 : len(arg)-1])
			if err != nil {
				return nil, err
			}

			step.conds = append(step.conds, destinationCond{regexp: re})
		} else {
			step.conds = append(step.conds, destinationCond{value: arg})
		}
	}

	for _, child := range node.Children {
		substep, err := StepFromCfg(child)
		if err != nil {
			return nil, err
		}
		step.substeps = append(step.substeps, substep)
	}

	return &step, nil
}

func StepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	switch node.Name {
	case "filter":
		return filterStepFromCfg(node)
	case "deliver":
		return deliverStepFromCfg(node)
	case "match":
		return matchStepFromCfg(node)
	case "destination":
		return destinationStepFromCfg(node)
	case "stop":
		return stopStep{}, nil
	case "require_auth":
		return requireAuthStep{}, nil
	case "check_source_hostname":
		return checkSourceHostnameStepFromCfg(node)
	case "check_source_mx":
		return checkSourceMxStepFromCfg(node)
	case "check_source_rdns":
		return checkSourceReverseDNSStepFromCfg(node)
	default:
		return nil, fmt.Errorf("unknown configuration directive or pipeline step: %s", node.Name)
	}
}
