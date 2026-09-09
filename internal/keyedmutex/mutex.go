// Package keyedmutex provides a keyed mutex implementation.
package keyedmutex

import "sync"

// MutexMap provides a map of mutexes keyed by string.
type MutexMap struct {
	mutexes  map[string]*sync.Mutex
	mapMutex *sync.Mutex
}

func NewMutexMap() *MutexMap {
	return &MutexMap{
		mutexes:  make(map[string]*sync.Mutex),
		mapMutex: &sync.Mutex{},
	}
}

// For returns the mutex for the given key, creating it if it doesn't exist.
func (km *MutexMap) For(key string) *sync.Mutex {
	km.mapMutex.Lock()
	defer km.mapMutex.Unlock()

	m, exists := km.mutexes[key]
	if !exists {
		m = &sync.Mutex{}
		km.mutexes[key] = m
	}
	return m
}
