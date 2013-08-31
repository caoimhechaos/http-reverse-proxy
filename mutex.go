package main

type Mutex struct {
	ch chan bool
}

func NewMutex() *Mutex {
	return &Mutex{ch: make(chan bool, 1)}
}

func (mu *Mutex) Lock() {
	mu.ch <- true
}

func (mu *Mutex) TryLock() bool {
	select {
	case mu.ch <- true:
		return true
	default:
		return false
	}
	return false
}

func (mu *Mutex) Unlock() {
	<-mu.ch
}
