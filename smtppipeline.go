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
	if len(node.Args) >= 0 {
		if node.Args[0] == "required" {
			required = true
		}
	}

	return checkSourceHostnameStep{
		required: required,
	}, nil
}

func (s checkSourceHostnameStep) Pass(ctx *module.DeliveryContext, _ io.Reader) (io.Reader, bool, error) {
	ipAddr, ok := ctx.SrcAddr.(*net.IPAddr)
	if !ok {
		return nil, true, nil
	}

	srcIPs, err := net.LookupIP(ctx.SrcHostname)
	if err != nil {
		ctx.Ctx["src_hostname_check"] = false
		if s.required {
			return nil, false, errors.New("source hostname does not resolve")
		} else {
			return nil, true, nil
		}
	}

	for _, ip := range srcIPs {
		if ipAddr.IP.Equal(ip) {
			ctx.Ctx["src_hostname_check"] = true
			return nil, true, nil
		}
	}
	ctx.Ctx["src_hostname_check"] = false
	if s.required {
		return nil, false, errors.New("source hostname does not resolve to source address")
	} else {
		return nil, true, nil
	}
}

type checkSourceMxStep struct {
	required bool
}

func checkSourceMxStepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	required := false
	if len(node.Args) >= 0 {
		if node.Args[0] == "required" {
			required = true
		}
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

	srcMx, err := net.LookupMX(domain)
	if err != nil {
		ctx.Ctx["src_mx_check"] = false
		if s.required {
			return nil, false, errors.New("could not find MX records for from domain")
		} else {
			return nil, true, nil
		}
	}

	for _, mx := range srcMx {
		if mx.Host == ctx.SrcHostname {
			ctx.Ctx["src_mx_check"] = true
			return nil, true, nil
		}
	}
	ctx.Ctx["src_mx_check"] = false
	if s.required {
		return nil, false, errors.New("From domain has no MX record for itself")
	} else {
		return nil, true, nil
	}
}

type checkSourceReverseDNSStep struct {
	required bool
}

func checkSourceReverseDNSStepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	required := false
	if len(node.Args) >= 0 {
		if node.Args[0] == "required" {
			required = true
		}
	}

	return checkSourceReverseDNSStep{
		required: required,
	}, nil
}

func (s checkSourceReverseDNSStep) Pass(ctx *module.DeliveryContext, _ io.Reader) (io.Reader, bool, error) {
	ipAddr, ok := ctx.SrcAddr.(*net.IPAddr)
	if !ok {
		return nil, true, nil
	}

	names, err := net.LookupAddr(ipAddr.IP.String())
	if err != nil || len(names) == 0 {
		ctx.Ctx["src_rdns_check"] = false
		if s.required {
			return nil, false, errors.New("could look up rDNS address for source")
		} else {
			return nil, true, nil
		}
	}

	srcDomain := strings.TrimRight(ctx.SrcHostname, ".")

	for _, name := range names {
		if strings.TrimRight(name, ".") == srcDomain {
			ctx.Ctx["src_rdns_check"] = true
			return nil, true, nil
		}
	}
	ctx.Ctx["src_rdns_check"] = false
	if s.required {
		return nil, false, errors.New("rDNS name does not match source hostname")
	} else {
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

	// TODO: Include reverse DNS information.
	received := "Received: from " + ctx.SrcHostname
	if tcpAddr, ok := ctx.SrcAddr.(*net.TCPAddr); ok {
		domain, err := LookupAddr(tcpAddr.IP)
		if err != nil {
			received += fmt.Sprintf(" ([%v])", tcpAddr.IP)
		} else {
			received += fmt.Sprintf(" (%s [%v])", domain, tcpAddr.IP)
		}
	}
	// TODO: Include our public IP address.
	received += fmt.Sprintf("\r\n\tby %s with %s", sanitizeString(ctx.OurHostname), ctx.SrcProto)
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

	currentMsg := msg
	var buf bytes.Buffer
	for _, substep := range step.substeps {
		r, cont, err := substep.Pass(ctx, currentMsg)
		if !cont {
			return nil, cont, err
		}

		if r != nil {
			if _, err := io.Copy(&buf, r); err != nil {
				return nil, false, err
			}
			currentMsg = &buf
		}
	}

	if currentMsg != msg {
		return currentMsg, true, nil
	}
	return msg, true, nil
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

func StepFromCfg(node config.Node) (SMTPPipelineStep, error) {
	switch node.Name {
	case "filter":
		return filterStepFromCfg(node)
	case "deliver":
		return deliverStepFromCfg(node)
	case "match":
		return matchStepFromCfg(node)
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
		return nil, fmt.Errorf("unknown pipeline step: %s", node.Name)
	}
}
