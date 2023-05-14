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
	"github.com/go-wares/log"
	"sync"
	"sync/atomic"
	"time"
)

type (
	// Keeper
	// 进程保持接口.
	Keeper interface {
		Name() string

		Add(s Keeper) bool
		AddWithDelete(p Keeper) bool
		Bind(p Keeper, del bool)
		Del(name string) bool
		Get(name string) (keeper Keeper, exists bool)

		After(es ...Event) Keeper
		Before(es ...Event) Keeper
		Listen(es ...Event) Keeper
		Panic(ep PanicEvent) Keeper

		Health() bool
		Restart()
		Start(ctx context.Context) (err error)
		Stop()
		Stopped() bool
	}

	keeper struct {
		mu            *sync.RWMutex
		name          string
		redo, running bool
		redoCount     uint64

		cancel     context.CancelFunc
		ctx        context.Context
		ea, eb, el []Event
		ep         PanicEvent

		parentKeeper Keeper
		parentUnset  bool
		subKeepers   map[string]Keeper
	}
)

func New(name string) Keeper {
	return (&keeper{name: name}).
		init().reset()
}

func (o *keeper) Name() string { return o.name }

// +---------------------------------------------------------------------------+
// | Add child keeper                                                          |
// +---------------------------------------------------------------------------+

func (o *keeper) Add(p Keeper) bool { return o.add(p, false) }

func (o *keeper) AddWithDelete(p Keeper) bool { return o.add(p, true) }

func (o *keeper) Bind(p Keeper, pu bool) {
	o.parentKeeper = p
	o.parentUnset = pu
}

func (o *keeper) Del(name string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.subKeepers[name]; ok {
		delete(o.subKeepers, name)
		return true
	}
	return false
}

func (o *keeper) Get(name string) (keeper Keeper, exists bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	keeper, exists = o.subKeepers[name]
	return
}

func (o *keeper) add(p Keeper, del bool) bool {
	o.mu.Lock()

	// 重复添加.
	if _, ok := o.subKeepers[p.Name()]; ok {
		o.mu.Unlock()
		return false
	}

	// 绑定父级.
	p.Bind(o, del)

	// 更新映射.
	o.subKeepers[p.Name()] = p
	o.mu.Unlock()
	log.Debugf("[keeper=%s] add child keeper: %s", o.name, p.Name())

	// 立即启动.
	if o.Health() {
		go o.childStart(p)
	}

	return true
}

// +---------------------------------------------------------------------------+
// | Event operations                                                          |
// +---------------------------------------------------------------------------+

func (o *keeper) After(ea ...Event) Keeper {
	o.ea = ea
	return o
}

func (o *keeper) Before(eb ...Event) Keeper {
	o.eb = eb
	return o
}

func (o *keeper) Listen(el ...Event) Keeper {
	o.el = el
	return o
}

func (o *keeper) Panic(ep PanicEvent) Keeper {
	o.ep = ep
	return o
}

// 启动子进程.
func (o *keeper) childStart(s Keeper) {
	if err := s.Start(o.ctx); err != nil {
		log.Errorf("[keeper=%s] child start: %v", o.name, err)
	}
}

// 启动全部子进程.
func (o *keeper) childStarts() {
	for _, s := range o.subKeepers {
		go o.childStart(s)
	}
}

// 等待子进程结束.
func (o *keeper) childWait() {
	for _, s := range o.subKeepers {
		if !s.Stopped() {
			time.Sleep(time.Millisecond * 10)
			o.childWait()
			return
		}
	}
}

func (o *keeper) contextBuild(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ctx, o.cancel = context.WithCancel(ctx)
}

func (o *keeper) contextDestroy() {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.ctx != nil && o.ctx.Err() == nil {
		o.cancel()
	}

	o.cancel = nil
	o.ctx = nil
}

func (o *keeper) contextRedo(redo bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.ctx != nil && o.ctx.Err() == nil {
		o.redo = redo
		o.cancel()
	}
}

func (o *keeper) runEvents(ctx context.Context, es []Event) (ignored bool) {
	// 捕获事件异常.
	defer func() {
		if v := recover(); v != nil {
			ignored = true

			if o.ep != nil {
				o.ep(ctx, v)
			} else {
				log.Fatalf("[keeper] run registered events fatal: %v", v)
			}
		}
	}()

	// 遍历事件.
	// 任一事件返回 true 时, 忽略后续事件.
	for _, e := range es {
		if ignored = e(ctx); ignored {
			break
		}
	}
	return
}

// +---------------------------------------------------------------------------+
// | Lifetime operations                                                       |
// +---------------------------------------------------------------------------+

func (o *keeper) Health() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()

	if o.ctx != nil && o.ctx.Err() == nil {
		return true
	}
	return false
}

func (o *keeper) Restart() { o.contextRedo(true) }

func (o *keeper) Start(ctx context.Context) (err error) {
	o.mu.Lock()

	// 重复启动.
	if o.running {
		o.mu.Unlock()
		err = ErrStartedAlready
		return
	}

	// 开始启动.
	o.running = true
	o.mu.Unlock()
	log.Debugf("[keeper=%s] start keeper", o.name)

	// 退出进程.
	defer func() {
		o.reset()
		log.Debugf("[keeper=%s] keeper stopped", o.name)
	}()

	// 前置事件.
	if ignored := o.runEvents(ctx, o.eb); ignored {
		err = ErrIgnoredBeforeEvents
		return
	}

	// 后置事件.
	defer func() {
		if ignored := o.runEvents(ctx, o.ea); ignored {
			err = ErrIgnoredAfterEvents
			return
		}
	}()

	// 主体事件.
	for {
		// 主上下文/退出.
		if ctx != nil && ctx.Err() != nil {
			break
		}

		// 主状态退出.
		if func() bool {
			o.mu.Lock()
			defer o.mu.Unlock()
			if o.redo {
				o.redo = false
				return false
			}
			return true
		}() {
			break
		}

		// 上下文/创建.
		rc := atomic.AddUint64(&o.redoCount, 1)

		log.Debugf("[keeper=%s] listen events called: count=%d", o.name, rc)
		o.contextBuild(ctx)

		// 上下文/主体.
		o.childStarts()
		o.runEvents(o.ctx, o.el)

		// 上下文/销毁.
		o.contextDestroy()

		// 上下文/等待子进程退出.
		o.childWait()
	}
	return
}

func (o *keeper) Stop() { o.contextRedo(false) }

func (o *keeper) Stopped() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()

	return !o.running
}

// +----------------------------------------------------------------------------
// | Event operations
// +----------------------------------------------------------------------------

func (o *keeper) init() *keeper {
	log.Debugf("[keeper=%s] initialized", o.name)

	o.mu = &sync.RWMutex{}
	o.subKeepers = make(map[string]Keeper)
	return o
}

func (o *keeper) reset() *keeper {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.redo = true
	o.running = false

	// 清除关系.
	if o.parentKeeper != nil && o.parentUnset {
		o.parentKeeper.Del(o.name)
		o.parentKeeper = nil
	}

	return o
}
