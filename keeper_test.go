// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// author: wsfuyibing <websearch@163.com>
// date: 2023-04-18

package process

import (
	"context"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	var (
		ctx, cancel = context.WithCancel(context.Background())
		k1          = ke("keeper-1")
		k2          = ke("keeper-2")
	)

	go func() {
		time.Sleep(time.Second * 1)
		k1.Restart()

		time.Sleep(time.Second * 3)
		cancel()
	}()

	k1.Add(k2)

	err := k1.Start(ctx)
	t.Logf("keeper: %v", err)
}

type (
	example struct {
		name string
	}
)

func (o *example) onAfter1(ctx context.Context) (ignored bool) { return }
func (o *example) onAfter2(ctx context.Context) (ignored bool) { return }

func (o *example) onBefore1(ctx context.Context) (ignored bool) { return }
func (o *example) onBefore2(ctx context.Context) (ignored bool) { return }

func (o *example) onListen1(ctx context.Context) (ignored bool) { return }
func (o *example) onListen2(ctx context.Context) (ignored bool) {
	for {
		select {
		case <-ctx.Done():
			return
		}
	}
}

func (o *example) onPanic(ctx context.Context, v interface{}) {}

func ke(name string) Keeper {
	e := &example{name: name}
	k := New(e.name).
		After(e.onAfter1, e.onAfter2).
		Before(e.onBefore1, e.onBefore2).
		Listen(e.onListen1, e.onListen2).
		Panic(e.onPanic)
	return k
}

func ex2() *example {
	ex := &example{name: "ex2"}
	return ex
}
