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
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/psrpc"
	"github.com/livekit/psrpc/internal"
	"github.com/livekit/psrpc/internal/bus"
	"github.com/livekit/psrpc/internal/logger"
	"github.com/livekit/psrpc/pkg/info"
	"github.com/livekit/psrpc/pkg/metadata"
)

// AffinityFunc 亲和力函数
type AffinityFunc[RequestType proto.Message] func(context.Context, RequestType) float32

// rpcHandlerImpl 是一个结构体，定义了RPC处理程序
type rpcHandlerImpl[RequestType proto.Message, ResponseType proto.Message] struct {
	i *info.RequestInfo // 请求信息

	handler      func(context.Context, RequestType) (ResponseType, error) // 处理程序
	affinityFunc AffinityFunc[RequestType]                                // 亲和力函数

	mu          sync.RWMutex
	requestSub  bus.Subscription[*internal.Request]       // 请求订阅
	claimSub    bus.Subscription[*internal.ClaimResponse] // 声明订阅
	claims      map[string]chan *internal.ClaimResponse   // 声明通道
	handling    sync.WaitGroup                            // 处理程序等待组
	closeOnce   sync.Once                                 // 关闭一次
	complete    chan struct{}                             // 完成通道
	onCompleted func()                                    // 完成回调
}

// newRPCHandler 创建一个RPC处理程序
func newRPCHandler[RequestType proto.Message, ResponseType proto.Message](
	s *RPCServer, // 服务器
	i *info.RequestInfo, // 请求信息
	svcImpl func(context.Context, RequestType) (ResponseType, error), // 服务实现
	interceptor psrpc.ServerRPCInterceptor, // 拦截器
	affinityFunc AffinityFunc[RequestType], // 亲和力函数
) (*rpcHandlerImpl[RequestType, ResponseType], error) {

	ctx := context.Background()

	var requestSub bus.Subscription[*internal.Request]
	var claimSub bus.Subscription[*internal.ClaimResponse]
	var err error

	if i.Queue {
		requestSub, err = bus.SubscribeQueue[*internal.Request](
			ctx, s.bus, i.GetRPCChannel(), s.ChannelSize,
		)
	} else {
		requestSub, err = bus.Subscribe[*internal.Request](
			ctx, s.bus, i.GetRPCChannel(), s.ChannelSize,
		)
	}
	if err != nil {
		return nil, err
	}

	if i.RequireClaim {
		claimSub, err = bus.Subscribe[*internal.ClaimResponse](
			ctx, s.bus, i.GetClaimResponseChannel(), s.ChannelSize,
		)
		if err != nil {
			_ = requestSub.Close()
			return nil, err
		}
	} else {
		claimSub = bus.EmptySubscription[*internal.ClaimResponse]{}
	}

	h := &rpcHandlerImpl[RequestType, ResponseType]{
		i:            i,
		requestSub:   requestSub,
		claimSub:     claimSub,
		claims:       make(map[string]chan *internal.ClaimResponse),
		affinityFunc: affinityFunc,
		complete:     make(chan struct{}),
	}

	if interceptor == nil {
		h.handler = svcImpl
	} else {
		h.handler = func(ctx context.Context, req RequestType) (ResponseType, error) {
			var response ResponseType
			res, err := interceptor(ctx, req, i.RPCInfo, func(context.Context, proto.Message) (proto.Message, error) {
				return svcImpl(ctx, req)
			})
			if res != nil {
				response = res.(ResponseType)
			}
			return response, err
		}
	}

	return h, nil
}

// run 运行RPC处理线程
func (h *rpcHandlerImpl[RequestType, ResponseType]) run(s *RPCServer) {
	go func() {
		requests := h.requestSub.Channel()
		claims := h.claimSub.Channel()

		for {
			select {
			case <-h.complete:
				return

			case ir := <-requests: // ir: internal.Request
				if ir == nil {
					continue
				}
				if time.Now().UnixNano() < ir.Expiry {
					go func() {
						if err := h.handleRequest(s, ir); err != nil {
							logger.Error(err, "failed to handle request", "requestID", ir.RequestId)
						}
					}()
				}

			case claim := <-claims:
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

// handleRequest 处理请求
func (h *rpcHandlerImpl[RequestType, ResponseType]) handleRequest(
	s *RPCServer,
	ir *internal.Request,
) error {
	h.handling.Add(1)
	defer h.handling.Done()

	head := &metadata.Header{
		RemoteID: ir.ClientId,
		SentAt:   time.Unix(0, ir.SentAt),
		Metadata: ir.Metadata,
	}
	ctx := metadata.NewContextWithIncomingHeader(context.Background(), head)
	ctx, cancel := context.WithDeadline(ctx, time.Unix(0, ir.Expiry))
	defer cancel()

	req, err := bus.DeserializePayload[RequestType](ir.RawRequest)
	if err != nil {
		var res ResponseType
		err = psrpc.NewError(psrpc.MalformedRequest, err)
		_ = h.sendResponse(s, ctx, ir, res, err) // 发送错误响应
		return err
	}

	if h.i.RequireClaim {
		// 如果需要声明，则进行声明请求
		claimed, err := h.claimRequest(s, ctx, ir, req)
		if err != nil {
			return err
		} else if !claimed {
			return nil
		}
		// 如果声明请求成功，则调用handler处理请求
	}

	// call handler function and return response
	response, err := h.handler(ctx, req)             // 调用handler处理请求 （调用时的业务处理？）
	return h.sendResponse(s, ctx, ir, response, err) // 发送响应
}

// claimRequest 响应一个声明请求，用于客户端选择一个服务器处理请求
func (h *rpcHandlerImpl[RequestType, ResponseType]) claimRequest(
	s *RPCServer,
	ctx context.Context,
	ir *internal.Request,
	req RequestType,
) (bool, error) {

	var affinity float32 = 1 // 默认亲和力为1
	if h.affinityFunc != nil {
		affinity = h.affinityFunc(ctx, req) // 根据请求计算亲和力，例如可以完成根据距离计算亲和力，或根据负载计算亲和力等等
		if affinity < 0 {
			return false, nil
		}
	}

	claimResponseChan := make(chan *internal.ClaimResponse, 1)

	h.mu.Lock()
	h.claims[ir.RequestId] = claimResponseChan
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.claims, ir.RequestId)
		h.mu.Unlock()
	}()

	// 发布声明请求
	err := s.bus.Publish(ctx, info.GetClaimRequestChannel(s.Name, ir.ClientId), &internal.ClaimRequest{
		RequestId: ir.RequestId,
		ServerId:  s.ID,
		Affinity:  affinity,
	})
	if err != nil {
		return false, err
	}

	timeout := time.NewTimer(time.Duration(ir.Expiry - time.Now().UnixNano()))
	defer timeout.Stop()

	select {
	case claim := <-claimResponseChan:
		if claim.ServerId == s.ID {
			return true, nil // 声明请求成功
		} else {
			return false, nil // 声明请求失败
		}

	case <-timeout.C:
		return false, nil // 声明请求超时（没有收到声明请求：失败）
	}
}

// sendResponse 发送响应
func (h *rpcHandlerImpl[RequestType, ResponseType]) sendResponse(
	s *RPCServer,
	ctx context.Context,
	ir *internal.Request,
	response proto.Message,
	err error,
) error {
	res := &internal.Response{
		RequestId: ir.RequestId,
		ServerId:  s.ID,
		SentAt:    time.Now().UnixNano(),
	}

	if err != nil {
		var e psrpc.Error

		if errors.As(err, &e) {
			res.Error = e.Error()
			res.Code = string(e.Code())
			res.ErrorDetails = append(res.ErrorDetails, e.DetailsProto()...)
		} else if st, ok := status.FromError(err); ok && st != nil && st.Code() != codes.OK {
			res.Error = st.Message()
			res.Code = string(psrpc.ErrorCodeFromGRPC(st.Code()))
			res.ErrorDetails = append(res.ErrorDetails, st.Proto().Details...)
		} else {
			res.Error = err.Error()
			res.Code = string(psrpc.Unknown)
		}
	} else if response != nil {
		b, err := bus.SerializePayload(response)
		if err != nil {
			res.Error = err.Error()
			res.Code = string(psrpc.MalformedResponse)
		} else {
			res.RawResponse = b
		}
	}

	return s.bus.Publish(ctx, info.GetResponseChannel(s.Name, ir.ClientId), res)
}

// close 关闭RPC处理程序
func (h *rpcHandlerImpl[RequestType, ResponseType]) close(force bool) {
	h.closeOnce.Do(func() {
		_ = h.requestSub.Close()
		if !force {
			h.handling.Wait()
		}
		_ = h.claimSub.Close()
		h.onCompleted()
		close(h.complete)
	})
	<-h.complete
}
