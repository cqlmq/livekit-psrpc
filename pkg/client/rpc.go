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

package client

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/psrpc"
	"github.com/livekit/psrpc/internal"
	"github.com/livekit/psrpc/internal/bus"
	"github.com/livekit/psrpc/internal/interceptors"
	"github.com/livekit/psrpc/pkg/info"
	"github.com/livekit/psrpc/pkg/metadata"
	"github.com/livekit/psrpc/pkg/rand"
)

// RequestSingle 发送单个请求, 返回单个响应
// 备注： 这个函数在psrpc中是核心函数，几乎所有请求都是通过这个函数发送的
func RequestSingle[ResponseType proto.Message](
	ctx context.Context,
	c *RPCClient,
	rpc string,
	topic []string,
	request proto.Message,
	opts ...psrpc.RequestOption,
) (response ResponseType, err error) {
	if c.closed.IsBroken() {
		err = psrpc.ErrClientClosed
		return
	}

	i := c.GetInfo(rpc, topic)

	// response hooks
	defer func() {
		for _, hook := range c.ResponseHooks {
			hook(ctx, request, i.RPCInfo, response, err)
		}
	}()

	// request hooks
	for _, hook := range c.RequestHooks {
		hook(ctx, request, i.RPCInfo)
	}

	reqInterceptors := getRequestInterceptors(
		c.RpcInterceptors,
		getRequestOpts(ctx, i, c.ClientOpts, opts...).Interceptors,
	)
	handler := interceptors.ChainClientInterceptors(
		reqInterceptors, i, newRPC[ResponseType](c, i),
	)

	res, err := handler(ctx, request, opts...)
	if res != nil {
		response, _ = res.(ResponseType)
	}

	return
}

// newRPC 创建一个RPC处理函数
func newRPC[ResponseType proto.Message](c *RPCClient, i *info.RequestInfo) psrpc.ClientRPCHandler {
	return func(ctx context.Context, request proto.Message, opts ...psrpc.RequestOption) (response proto.Message, err error) {
		o := getRequestOpts(ctx, i, c.ClientOpts, opts...)

		b, err := bus.SerializePayload(request)
		if err != nil {
			err = psrpc.NewError(psrpc.MalformedRequest, err)
			return
		}

		requestID := rand.NewRequestID()
		now := time.Now()

		// 创建一个请求对象
		req := &internal.Request{
			RequestId:  requestID,
			ClientId:   c.ID,
			SentAt:     now.UnixNano(),
			Expiry:     now.Add(o.Timeout).UnixNano(),
			Multi:      false,
			RawRequest: b,
			Metadata:   metadata.OutgoingContextMetadata(ctx),
		}

		var claimChan chan *internal.ClaimRequest   // 声明请求通道
		resChan := make(chan *internal.Response, 1) // 响应通道

		c.mu.Lock()
		if i.RequireClaim {
			claimChan = make(chan *internal.ClaimRequest, c.ChannelSize)
			c.claimRequests[requestID] = claimChan
		}
		c.responseChannels[requestID] = resChan
		c.mu.Unlock()

		defer func() {
			c.mu.Lock()
			if i.RequireClaim {
				delete(c.claimRequests, requestID)
			}
			delete(c.responseChannels, requestID)
			c.mu.Unlock()
		}()

		// 将请求发布到通道， 备注： 所有服务器收到，但暂不处理，而是会发出声明请求（我可以处理）？ 然后在selectServer中选择一个服务器处理
		if err = c.bus.Publish(ctx, i.GetRPCChannel(), req); err != nil {
			err = psrpc.NewError(psrpc.Internal, err)
			return
		}

		ctx, cancel := context.WithTimeout(ctx, o.Timeout)
		defer cancel()

		// 如果需要声明请求 （以后研究这个有什么作用）
		if i.RequireClaim {
			// 选择一个服务器
			serverID, err := selectServer(ctx, claimChan, resChan, o.SelectionOpts)
			if err != nil {
				return nil, err
			}

			// 将声明请求发布到通道（我选择了你，你来处理？）
			if err = c.bus.Publish(ctx, i.GetClaimResponseChannel(), &internal.ClaimResponse{
				RequestId: requestID,
				ServerId:  serverID,
			}); err != nil {
				err = psrpc.NewError(psrpc.Internal, err)
				return nil, err
			}
		}

		select {
		case res := <-resChan:
			if res.Error != "" {
				err = psrpc.NewErrorFromResponse(res.Code, res.Error, res.ErrorDetails...)
			} else {
				response, err = bus.DeserializePayload[ResponseType](res.RawResponse)
				if err != nil {
					err = psrpc.NewError(psrpc.MalformedResponse, err)
				}
			}

		case <-ctx.Done():
			err = ctx.Err()
			if errors.Is(err, context.Canceled) {
				err = psrpc.ErrRequestCanceled
			} else if errors.Is(err, context.DeadlineExceeded) {
				err = psrpc.ErrRequestTimedOut
			}
		}

		return
	}
}

// selectServer 选择一个服务器
func selectServer(
	ctx context.Context,
	claimChan chan *internal.ClaimRequest,
	resChan chan *internal.Response,
	opts psrpc.SelectionOpts,
) (string, error) {

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if opts.AffinityTimeout > 0 {
		// 设置一个超时时间, 如果超时则取消
		time.AfterFunc(opts.AffinityTimeout, cancel)
	}

	var (
		shorted    bool           // 短路
		serverID   string         // 服务器ID
		affinity   float32        // 亲和力
		claims     []*psrpc.Claim // 声明请求
		claimCount int            // 声明请求计数
		resErr     error          // 错误
	)

	for {
		select {
		case <-ctx.Done():
			switch {
			case opts.SelectionFunc != nil:
				return opts.SelectionFunc(claims)
			case serverID != "":
				return serverID, nil
			case resErr != nil:
				return "", resErr
			case claimCount > 0:
				return "", psrpc.NewErrorf(psrpc.Unavailable, "no servers available (received %d responses)", claimCount)
			default:
				return "", psrpc.ErrNoResponse
			}

		case claim := <-claimChan:
			claimCount++
			if (opts.MinimumAffinity > 0 && claim.Affinity >= opts.MinimumAffinity) || opts.MinimumAffinity <= 0 {
				if opts.AcceptFirstAvailable || opts.MaximumAffinity > 0 && claim.Affinity >= opts.MaximumAffinity {
					return claim.ServerId, nil
				}

				if opts.SelectionFunc != nil {
					claims = append(claims, &psrpc.Claim{ServerID: claim.ServerId, Affinity: claim.Affinity})
				} else if claim.Affinity > affinity {
					// 如果亲和力大于当前亲和力，则更新服务器ID和亲和力，（找到亲和力最高的）
					// 备注：亲和力是服务器性能的指标，亲和力越高，服务器性能越好
					serverID = claim.ServerId
					affinity = claim.Affinity
				}

				if opts.ShortCircuitTimeout > 0 && !shorted {
					// 如果设置了短路超时，则设置一个超时时间，如果超时则取消
					shorted = true
					time.AfterFunc(opts.ShortCircuitTimeout, cancel)
				}
			}

		case res := <-resChan:
			// will only happen with malformed requests // 只有当请求格式错误时才会发生
			if res.Error != "" {
				resErr = psrpc.NewErrorf(psrpc.ErrorCode(res.Code), res.Error)
			}
		}
	}
}
