/*
Copyright 2026 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sessionmanager

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBindingStoreExpiryAndCapacity(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	store := newBindingStore(time.Minute, 2)
	store.now = func() time.Time { return now }
	store.put("one", "tag", "model")
	now = now.Add(time.Second)
	store.put("two", "tag", "model")
	now = now.Add(time.Second)
	store.put("three", "tag", "model")

	_, known, _, _ := store.observe("one", "model")
	assert.False(t, known)
	_, known, duplicate, mismatch := store.observe("three", "model")
	assert.True(t, known)
	assert.False(t, duplicate)
	assert.False(t, mismatch)
	_, known, duplicate, mismatch = store.observe("three", "model")
	assert.True(t, known)
	assert.True(t, duplicate)
	assert.False(t, mismatch)

	now = now.Add(time.Minute)
	assert.Zero(t, store.len())
}

func TestStampGeneratorConcurrentUniqueness(t *testing.T) {
	t.Parallel()
	generator, err := newStampGenerator()
	require.NoError(t, err)
	const count = 1000
	stamps := sync.Map{}
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			stamp, stampErr := generator.next()
			require.NoError(t, stampErr)
			_, loaded := stamps.LoadOrStore(stamp, struct{}{})
			assert.False(t, loaded)
		})
	}
	wg.Wait()
}

func TestStampGeneratorRejectsWrap(t *testing.T) {
	t.Parallel()
	generator, err := newStampGenerator()
	require.NoError(t, err)
	generator.counter.Store(math.MaxUint64)
	_, err = generator.next()
	require.Error(t, err)
}
