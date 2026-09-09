package keyedmutex

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMutexMap_NewMutexMap(t *testing.T) {
	km := NewMutexMap()
	require.NotNil(t, km.mutexes)
	require.NotNil(t, km.mapMutex)
	require.Len(t, km.mutexes, 0)
}

func TestMutexMap_For(t *testing.T) {
	km := NewMutexMap()

	t.Run("same key returns same mutex", func(t *testing.T) {
		m1 := km.For("test-key")
		m2 := km.For("test-key")
		require.Same(t, m1, m2)
	})

	t.Run("different keys return different mutexes", func(t *testing.T) {
		m1 := km.For("key-one")
		m2 := km.For("key-two")
		require.NotSame(t, m1, m2)
	})

	t.Run("mutexes are independent", func(t *testing.T) {
		m1 := km.For("key-one")
		m2 := km.For("key-two")

		done := make(chan bool, 2)
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			m1.Lock()
			defer m1.Unlock()
			done <- true
		}()

		go func() {
			defer wg.Done()
			m2.Lock()
			defer m2.Unlock()
			done <- true
		}()

		wg.Wait()
		close(done)

		require.Len(t, done, 2)
	})

	t.Run("concurrent For calls do not panic", func(t *testing.T) {
		var wg sync.WaitGroup
		keys := []string{"a", "b", "c", "d", "e"}

		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				_ = km.For(keys[idx%len(keys)])
			}(i)
		}

		wg.Wait()
	})

	t.Run("subsequent calls on same key return same mutex", func(t *testing.T) {
		km2 := NewMutexMap()
		keys := []string{"x", "y", "z"}
		first := make([]*sync.Mutex, 0, len(keys))
		second := make([]*sync.Mutex, 0, len(keys))

		for _, k := range keys {
			first = append(first, km2.For(k))
		}
		for _, k := range keys {
			second = append(second, km2.For(k))
		}

		for i := range keys {
			require.Same(t, first[i], second[i])
		}
	})
}
