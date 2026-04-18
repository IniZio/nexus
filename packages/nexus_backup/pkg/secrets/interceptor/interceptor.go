package interceptor

import "log"

type Interceptor struct {
	// Placeholder for syscall interception
}

func New() *Interceptor {
	return &Interceptor{}
}

func (i *Interceptor) Start() error {
	log.Println("[interceptor] HTTP interception not yet implemented")
	log.Println("[interceptor] Tokens are available via vending server only")
	return nil
}

func (i *Interceptor) Stop() error {
	return nil
}
