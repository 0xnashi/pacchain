package pac

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/0xnashi/pacchain/pacfunc"
	"github.com/robertkrimen/otto"
)

// OttoEngineOpt used to configure an OttoEngine via the NewOttoEngine func
type OttoEngineOpt func(*OttoEngine)

// OttoLoader that the engine should use
func OttoLoader(fn Loader) OttoEngineOpt {
	return func(o *OttoEngine) {
		o.loader = fn
	}
}

// DefaultTimeout bounds one PAC evaluation. A normal PAC answers in well
// under a millisecond; this only catches broken or hostile PAC files.
// Note: a Go helper such as dnsResolve() cannot be interrupted while it
// blocks; the limit applies as soon as it returns to JavaScript.
const DefaultTimeout = 3 * time.Second

// ErrPACTimeout is returned when PAC code runs longer than its timeout.
var ErrPACTimeout = errors.New("PAC evaluation timed out")

// OttoTimeout bounds each PAC evaluation and the initial PAC load.
func OttoTimeout(d time.Duration) OttoEngineOpt {
	return func(o *OttoEngine) {
		o.timeout = d
	}
}

// OttoStringLoader implements a string loader
func OttoStringLoader(pac string) OttoEngineOpt {
	return OttoLoader(func() (string, error) {
		return pac, nil
	})
}

// NewOttoEngine instance with configuration
func NewOttoEngine(opts ...OttoEngineOpt) *OttoEngine {
	otto := &OttoEngine{
		mutex:   &sync.RWMutex{},
		timeout: DefaultTimeout,
		loader: func() (string, error) {
			return "", errors.New("pac loader has not been configured")
		},
	}
	for _, opt := range opts {
		opt(otto)
	}
	return otto
}

// OttoEngine struct
type OttoEngine struct {
	timeout   time.Duration
	mutex     *sync.RWMutex
	loader    Loader
	isStarted bool
	vm        *otto.Otto
}

func (o *OttoEngine) Start() error {
	defer func() {
		o.mutex.RLock()
		defer o.mutex.RUnlock()
		if o.isStarted {
			log.Print("started OttoEngine")
		} else {
			log.Print("failed to start OttoEngine")
		}
	}()
	o.mutex.RLock()
	if o.isStarted {
		defer o.mutex.RUnlock()
		return nil
	}
	o.mutex.RUnlock()
	o.mutex.Lock()
	defer o.mutex.Unlock()
	if o.isStarted {
		return nil
	}
	log.Print("initialising OttoEngine")
	vm := otto.New()

	// ConvertAddr(ipaddr string)
	vm.Set("convert_addr", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		var (
			ipaddr string
			err    error
		)
		if ipaddr, err = call.Argument(0).ToString(); err != nil {
			return
		}
		if v, err := vm.ToValue(pacfunc.ConvertAddr(ipaddr)); err == nil {
			value = v
		}
		return
	})

	// DNSDomainIs(host, domain string) bool
	vm.Set("dnsDomainIs", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		var (
			host   string
			domain string
			err    error
		)
		if host, err = call.Argument(0).ToString(); err != nil {
			return
		}
		if domain, err = call.Argument(1).ToString(); err != nil {
			return
		}
		if v, err := vm.ToValue(pacfunc.DNSDomainIs(host, domain)); err == nil {
			value = v
		}
		return
	})

	// ShExpMatch(str, shexp string) bool
	vm.Set("shExpMatch", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		var (
			str   string
			shexp string
			err   error
		)
		if str, err = call.Argument(0).ToString(); err != nil {
			return
		}
		if shexp, err = call.Argument(1).ToString(); err != nil {
			return
		}
		if v, err := vm.ToValue(pacfunc.ShExpMatch(str, shexp)); err == nil {
			value = v
		}
		return
	})

	// IsInNet(host, netip, netmask string) bool
	vm.Set("isInNet", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		var (
			host    string
			netip   string
			netmask string
			err     error
		)
		if host, err = call.Argument(0).ToString(); err != nil {
			return
		}
		if netip, err = call.Argument(1).ToString(); err != nil {
			return
		}
		if netmask, err = call.Argument(2).ToString(); err != nil {
			return
		}
		if v, err := vm.ToValue(pacfunc.IsInNet(host, netip, netmask)); err == nil {
			value = v
		}
		return
	})

	// MyIPAddress() string
	vm.Set("myIpAddress", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.NullValue()
		if v, err := vm.ToValue(pacfunc.MyIPAddress()); err == nil {
			value = v
		}
		return
	})

	// DNSResolve(host string) string
	vm.Set("dnsResolve", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		var (
			host string
			err  error
		)
		if host, err = call.Argument(0).ToString(); err != nil {
			return
		}
		if v, err := vm.ToValue(pacfunc.DNSResolve(host)); err == nil {
			value = v
		}
		return
	})

	// IsPlainHostName(host string) bool
	vm.Set("isPlainHostName", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		var (
			host string
			err  error
		)
		if host, err = call.Argument(0).ToString(); err != nil {
			return
		}
		if v, err := vm.ToValue(pacfunc.IsPlainHostName(host)); err == nil {
			value = v
		}
		return
	})

	// LocalHostOrDomainIs(host, hostdom string) bool
	vm.Set("localHostOrDomainIs", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		var (
			host    string
			hostdom string
			err     error
		)
		if host, err = call.Argument(0).ToString(); err != nil {
			return
		}
		if hostdom, err = call.Argument(1).ToString(); err != nil {
			return
		}
		if v, err := vm.ToValue(pacfunc.LocalHostOrDomainIs(host, hostdom)); err == nil {
			value = v
		}
		return
	})

	// IsResolvable(host string) bool
	vm.Set("isResolvable", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		var (
			host string
			err  error
		)
		if host, err = call.Argument(0).ToString(); err != nil {
			return
		}
		if v, err := vm.ToValue(pacfunc.IsResolvable(host)); err == nil {
			value = v
		}
		return
	})

	// DNSDomainLevels(host string) int
	vm.Set("dnsDomainLevels", func(call otto.FunctionCall) (value otto.Value) {
		value, _ = otto.ToValue(0)
		var (
			host string
			err  error
		)
		if host, err = call.Argument(0).ToString(); err != nil {
			return
		}
		if v, err := vm.ToValue(pacfunc.DNSDomainLevels(host)); err == nil {
			value = v
		}
		return
	})

	// WeekdayRange(wd1, wd2, gmt string) bool
	vm.Set("weekdayRange", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		var (
			wd1, wd2, gmt string
		)
		if call.Argument(0).IsDefined() {
			wd1, _ = call.Argument(0).ToString()
		} else {
			wd1 = ""
		}
		if call.Argument(1).IsDefined() {
			wd2, _ = call.Argument(1).ToString()
		} else {
			wd2 = ""
		}
		if call.Argument(2).IsDefined() {
			gmt, _ = call.Argument(2).ToString()
		} else {
			gmt = ""
		}
		if v, err := vm.ToValue(pacfunc.WeekdayRange(wd1, wd2, gmt)); err == nil {
			value = v
		}
		return
	})

	// DateRange(args []string) bool
	vm.Set("dateRange", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		args := make([]string, len(call.ArgumentList))
		for i := 0; i < len(call.ArgumentList); i++ {
			args[i], _ = call.ArgumentList[i].ToString()
		}
		if v, err := vm.ToValue(pacfunc.DateRange(args)); err == nil {
			value = v
		}
		return
	})

	// TimeRange(args []string) bool
	vm.Set("timeRange", func(call otto.FunctionCall) (value otto.Value) {
		value = otto.FalseValue()
		args := make([]string, len(call.ArgumentList))
		for i := 0; i < len(call.ArgumentList); i++ {
			args[i], _ = call.ArgumentList[i].ToString()
		}
		if v, err := vm.ToValue(pacfunc.TimeRange(args)); err == nil {
			value = v
		}
		return
	})

	{
		pac, pacError := o.loader()
		if pacError != nil {
			return pacError
		}
		// Never log the PAC source: real PAC files list internal hostnames.
		log.Printf("PAC loaded (%d bytes)", len(pac))
		_, pacError = runLimited(vm, o.timeout, func() (otto.Value, error) { return vm.Run(pac) })
		if pacError != nil {
			return pacError
		}
	}

	o.vm = vm
	o.isStarted = true

	return nil
}

func (o *OttoEngine) Stop() error {
	defer func() {
		o.mutex.RLock()
		defer o.mutex.RUnlock()
		if !o.isStarted {
			log.Print("stopped OttoEngine")
		} else {
			log.Print("failed to stop OttoEngine")
		}
	}()
	o.mutex.RLock()
	if !o.isStarted {
		defer o.mutex.RUnlock()
		return nil
	}
	o.mutex.RUnlock()
	o.mutex.Lock()
	defer o.mutex.Unlock()
	if !o.isStarted {
		return nil
	}
	log.Print("stopping OttoEngine")
	o.vm = nil
	o.isStarted = false
	return nil
}

func (o *OttoEngine) Reload() error {
	if err := o.Stop(); err != nil {
		return err
	}
	if err := o.Start(); err != nil {
		return err
	}
	return nil
}

func (o *OttoEngine) FindProxyForURL(in *url.URL) (Proxies, error) {
	o.mutex.Lock()
	defer o.mutex.Unlock()

	value, err := runLimited(o.vm, o.timeout, func() (otto.Value, error) {
		return o.vm.Call("FindProxyForURL", nil, in.String(), in.Hostname())
	})
	if err != nil {
		return Proxies{}, err
	}

	findProxyString, err := otto.Value.ToString(value)
	if err != nil {
		return Proxies{}, err
	}

	return ParseFindProxyString(findProxyString)
}

// interrupted is the panic value used to stop a running PAC. otto re-panics
// values that are not its own errors, so runLimited can recover it.
type interrupted struct{}

// runLimited runs fn on vm, interrupting it after d. otto checks vm.Interrupt
// before every statement, so even `while (true) {}` stops. The channel is
// per call and never closed: otto would call the nil func a closed channel
// yields.
func runLimited(vm *otto.Otto, d time.Duration, fn func() (otto.Value, error)) (value otto.Value, err error) {
	if d <= 0 {
		return fn()
	}
	interrupt := make(chan func(), 1)
	vm.Interrupt = interrupt
	timer := time.AfterFunc(d, func() {
		interrupt <- func() { panic(interrupted{}) }
	})
	defer func() {
		timer.Stop()
		vm.Interrupt = nil // a late interrupt stays in the abandoned channel
		if r := recover(); r != nil {
			if _, ok := r.(interrupted); ok {
				err = fmt.Errorf("%w after %s", ErrPACTimeout, d)
				return
			}
			panic(r)
		}
	}()
	return fn()
}
