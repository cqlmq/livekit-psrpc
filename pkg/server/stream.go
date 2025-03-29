// Copyright 2023 LiveKit, Inc.
//
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

package server

import (
	"context"
	"sync"
	"time"

	"go.uber.org/atomic"
	"golang.org/x/exp/maps"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/psrpc"
	"github.com/livekit/psrpc/internal"
	"github.com/livekit/psrpc/internal/bus"
	"github.com/livekit/psrpc/internal/logger"
	"github.com/livekit/psrpc/internal/stream"
	"github.com/livekit/psrpc/pkg/info"
	"github.com/livekit/psrpc/pkg/metadata"
)

// StreamAffinityFunc 流亲和力函数
type StreamAffinityFunc func(ctx context.Context) float32

// streamHandler 流处理程序
type streamHandler[RecvType, SendType proto.Message] struct {
	i *info.RequestInfo // 请求信息

	handler func(psrpc.ServerStream[SendType, RecvType]) error // 处理函数
	// interceptors []psrpc.StreamInterceptor
	affinityFunc StreamAffinityFunc // 亲和力函数

	mu          sync.RWMutex
	streamSub   bus.Subscription[*internal.Stream]           // 流订阅
	claimSub    bus.Subscription[*internal.ClaimResponse]    // 声明订阅
	streams     map[string]stream.Stream[SendType, RecvType] // 流集合
	claims      map[string]chan *internal.ClaimResponse      // 声明集合
	draining    atomic.Bool                                  // 是否正在关闭
	closeOnce   sync.Once                                    // 关闭一次
	complete    chan struct{}                                // 完成通道
	onCompleted func()                                       // 完成回调
}

// newStreamRPCHandler 创建一个流处理程序
func newStreamRPCHandler[RecvType, SendType proto.Message](
	s *RPCServer,
	i *info.RequestInfo,
	svcImpl func(psrpc.ServerStream[SendType, RecvType]) error,
	affinityFunc StreamAffinityFunc,
) (*streamHandler[RecvType, SendType], error) {

	ctx := context.Background()
	streamSub, err := bus.Subscribe[*internal.Stream](
		ctx, s.bus, i.GetStreamServerChannel(), s.ChannelSize,
	)
	if err != nil {
		return nil, err
	}

	var claimSub bus.Subscription[*internal.ClaimResponse]
	if i.RequireClaim {
		claimSub, err = bus.Subscribe[*internal.ClaimResponse](
			ctx, s.bus, i.GetClaimResponseChannel(), s.ChannelSize,
		)
		if err != nil {
			_ = streamSub.Close()
			return nil, err
		}
	} else {
		claimSub = bus.EmptySubscription[*internal.ClaimResponse]{}
	}

	h := &streamHandler[RecvType, SendType]{
		i:            i,
		streamSub:    streamSub,
		claimSub:     claimSub,
		streams:      make(map[string]stream.Stream[SendType, RecvType]),
		claims:       make(map[string]chan *internal.ClaimResponse),
		affinityFunc: affinityFunc,
		handler:      svcImpl,
		complete:     make(chan struct{}),
	}
	return h, nil
}

// run 运行一个线程，处理请求，一个注册handler对应一个线程?
func (h *streamHandler[RecvType, SendType]) run(s *RPCServer) {
	go func() {
		requests := h.streamSub.Channel()
		claims := h.claimSub.Channel()

		for {
			select {
			case <-h.complete: // 完成
				return

			case is := <-requests: // 流请求 is: internal.Stream
				if is == nil {
					continue
				}
				if time.Now().UnixNano() < is.Expiry {
					if err := h.handleRequest(s, is); err != nil {
						logger.Error(err, "failed to handle request", "requestID", is.RequestId)
					}
				}

			case claim := <-claims: // 声明 claim: internal.ClaimResponse
				if claim == nil {
					continue
				}
				h.mu.RLock()
				claimChan, ok := h.claims[claim.RequestId]
				h.mu.RUnlock()
				if ok {
					claimChan <- claim
				}
			}
		}
	}()
}

// handleRequest 处理流请求
func (h *streamHandler[RecvType, SendType]) handleRequest(
	s *RPCServer,
	is *internal.Stream,
) error {
	if open := is.GetOpen(); open != nil {
		if h.draining.Load() {
			return nil
		}

		go func() {
			// 处理流打开请求
			if err := h.handleOpenRequest(s, is, open); err != nil {
				logger.Error(err, "stream handler failed", "requestID", is.RequestId)
			}
		}()
	} else {
		h.mu.Lock()
		ss, ok := h.streams[is.StreamId]
		h.mu.Unlock()

		if ok {
			return ss.HandleStream(is)
		}
	}
	return nil
}

// handleOpenRequest 处理流打开请求
func (h *streamHandler[RecvType, SendType]) handleOpenRequest(
	s *RPCServer,
	is *internal.Stream,
	open *internal.StreamOpen,
) error {
	head := &metadata.Header{
		RemoteID: open.NodeId,
		SentAt:   time.Unix(0, is.SentAt),
		Metadata: open.Metadata,
	}
	ctx := metadata.NewContextWithIncomingHeader(context.Background(), head)
	octx, cancel := context.WithDeadline(ctx, time.Unix(0, is.Expiry))
	defer cancel()

	if h.i.RequireClaim {
		claimed, err := h.claimRequest(s, octx, is)
		if !claimed {
			return err
		}
	}

	ss := stream.NewStream[SendType, RecvType]( // RecvType可以从recvChan中推断出来，所以可以省略
		ctx,
		h.i,
		is.StreamId,
		s.Timeout,
		&serverStream[RecvType, SendType]{
			h:      h,
			s:      s,
			nodeID: open.NodeId,
		},
		s.StreamInterceptors,
		make(chan RecvType, s.ChannelSize),
		make(map[string]chan struct{}),
	)

	h.mu.Lock()
	h.streams[is.StreamId] = ss
	h.mu.Unlock()

	// 确认流请求
	if err := ss.Ack(octx, is); err != nil {
		_ = ss.Close(err)
		return err
	}

	// 处理流
	err := h.handler(ss)
	if !ss.Hijacked() {
		_ = ss.Close(err)
	}

	return nil
}

func (h *streamHandler[RecvType, SendType]) claimRequest(
	s *RPCServer,
	ctx context.Context,
	is *internal.Stream,
) (bool, error) {

	var affinity float32
	if h.affinityFunc != nil {
		affinity = h.affinityFunc(ctx)
		if affinity < 0 {
			return false, nil
		}
	} else {
		affinity = 1
	}

	claimResponseChan := make(chan *internal.ClaimResponse, 1)

	h.mu.Lock()
	h.claims[is.RequestId] = claimResponseChan
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.claims, is.RequestId)
		h.mu.Unlock()
	}()

	err := s.bus.Publish(ctx, info.GetClaimRequestChannel(s.Name, is.GetOpen().NodeId), &internal.ClaimRequest{
		RequestId: is.RequestId,
		ServerId:  s.ID,
		Affinity:  affinity,
	})
	if err != nil {
		return false, err
	}

	timeout := time.NewTimer(time.Duration(is.Expiry - time.Now().UnixNano()))
	defer timeout.Stop()

	select {
	case claim := <-claimResponseChan:
		if claim.ServerId == s.ID {
			return true, nil
		} else {
			return false, nil
		}

	case <-timeout.C:
		return false, nil
	}
}

func (h *streamHandler[RecvType, SendType]) close(force bool) {
	h.closeOnce.Do(func() {
		h.draining.Store(true)

		h.mu.Lock()
		serverStreams := maps.Values(h.streams)
		h.mu.Unlock()

		var wg sync.WaitGroup
		for _, s := range serverStreams {
			wg.Add(1)
			s := s
			go func() {
				_ = s.Close(psrpc.ErrStreamEOF)
				wg.Done()
			}()
		}
		wg.Wait()

		_ = h.streamSub.Close()
		_ = h.claimSub.Close()
		h.onCompleted()
		close(h.complete)
	})
	<-h.complete
}

// serverStream 服务器流
type serverStream[SendType, RecvType proto.Message] struct {
	h      *streamHandler[SendType, RecvType] // 流处理程序
	s      *RPCServer                         // 服务器
	nodeID string                             // 节点ID
}

// Send 发送
func (s *serverStream[RequestType, ResponseType]) Send(ctx context.Context, msg *internal.Stream) (err error) {
	if err = s.s.bus.Publish(ctx, info.GetStreamChannel(s.s.Name, s.nodeID), msg); err != nil {
		err = psrpc.NewError(psrpc.Internal, err)
	}
	return
}

// Close 关闭
func (s *serverStream[RequestType, ResponseType]) Close(streamID string) {
	s.h.mu.Lock()
	delete(s.h.streams, streamID)
	s.h.mu.Unlock()
}
