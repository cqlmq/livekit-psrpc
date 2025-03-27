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

package psrpc

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/twitchtv/twirp"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

var (
	ErrRequestCanceled = NewErrorf(Canceled, "request canceled")
	ErrRequestTimedOut = NewErrorf(DeadlineExceeded, "request timed out")
	ErrNoResponse      = NewErrorf(Unavailable, "no response from servers")
	ErrStreamEOF       = NewError(Unavailable, io.EOF)
	ErrClientClosed    = NewErrorf(Canceled, "client is closed")
	ErrServerClosed    = NewErrorf(Canceled, "server is closed")
	ErrStreamClosed    = NewErrorf(Canceled, "stream closed")
	ErrSlowConsumer    = NewErrorf(Unavailable, "stream message discarded by slow consumer")
)

// Error 是psrpc的错误接口
type Error interface {
	error                       // 实现error接口
	Code() ErrorCode            // 返回错误代码
	Details() []any             // 返回错误详情
	DetailsProto() []*anypb.Any // 返回错误详情（protobuf格式）
	// 便利方法
	ToHttp() int                // 转换为HTTP状态码
	GRPCStatus() *status.Status // 转换为GRPC状态
}

// ErrorCode 是错误代码类型
type ErrorCode string

// Error 实现error类似的功能，返回错误代码的字符串表示
func (e ErrorCode) Error() string {
	return string(e)
}

// ToHTTP 将错误代码转换为HTTP状态码
func (e ErrorCode) ToHTTP() int {
	switch e {
	case OK:
		return http.StatusOK // 成功
	case Unknown, MalformedResponse, Internal, DataLoss:
		return http.StatusInternalServerError // 服务器错误
	case InvalidArgument, MalformedRequest:
		return http.StatusBadRequest // 请求错误
	case NotFound:
		return http.StatusNotFound // 未找到
	case NotAcceptable:
		return http.StatusNotAcceptable // 不接受
	case AlreadyExists, Aborted:
		return http.StatusConflict // 冲突
	case PermissionDenied:
		return http.StatusForbidden // 禁止
	case ResourceExhausted:
		return http.StatusTooManyRequests // 太多请求
	case FailedPrecondition:
		return http.StatusPreconditionFailed // 预条件失败
	case OutOfRange:
		return http.StatusRequestedRangeNotSatisfiable // 请求范围不满足
	case Unimplemented:
		return http.StatusNotImplemented // 未实现
	case Canceled, DeadlineExceeded, Unavailable:
		return http.StatusServiceUnavailable // 服务不可用
	case Unauthenticated:
		return http.StatusUnauthorized // 未授权
	default:
		return http.StatusInternalServerError // 服务器错误
	}
}

// ErrorCodeFromGRPC 将GRPC错误代码转换为psrpc错误代码
// 目前变量名称是一一对应的，方便使用。 grpc中是uint32，psrpc中是ErrorCode(string)
func ErrorCodeFromGRPC(code codes.Code) ErrorCode {
	switch code {
	case codes.OK:
		return OK
	case codes.Canceled:
		return Canceled
	case codes.Unknown:
		return Unknown
	case codes.InvalidArgument:
		return InvalidArgument
	case codes.DeadlineExceeded:
		return DeadlineExceeded
	case codes.NotFound:
		return NotFound
	case codes.AlreadyExists:
		return AlreadyExists
	case codes.PermissionDenied:
		return PermissionDenied
	case codes.ResourceExhausted:
		return ResourceExhausted
	case codes.FailedPrecondition:
		return FailedPrecondition
	case codes.Aborted:
		return Aborted
	case codes.OutOfRange:
		return OutOfRange
	case codes.Unimplemented:
		return Unimplemented
	case codes.Internal:
		return Internal
	case codes.Unavailable:
		return Unavailable
	case codes.DataLoss:
		return DataLoss
	case codes.Unauthenticated:
		return Unauthenticated
	default:
		return Unknown
	}
}

// ToGRPC 将psrpc错误代码转换为GRPC错误代码
func (e ErrorCode) ToGRPC() codes.Code {
	switch e {
	case OK:
		return codes.OK
	case Canceled:
		return codes.Canceled
	case Unknown:
		return codes.Unknown
	case InvalidArgument, MalformedRequest:
		return codes.InvalidArgument
	case DeadlineExceeded:
		return codes.DeadlineExceeded
	case NotFound:
		return codes.NotFound
	case AlreadyExists:
		return codes.AlreadyExists
	case PermissionDenied:
		return codes.PermissionDenied
	case ResourceExhausted:
		return codes.ResourceExhausted
	case FailedPrecondition:
		return codes.FailedPrecondition
	case Aborted:
		return codes.Aborted
	case OutOfRange:
		return codes.OutOfRange
	case Unimplemented:
		return codes.Unimplemented
	case MalformedResponse, Internal:
		return codes.Internal
	case Unavailable:
		return codes.Unavailable
	case DataLoss:
		return codes.DataLoss
	case Unauthenticated:
		return codes.Unauthenticated
	default:
		return codes.Unknown
	}
}

// ToTwirp 将psrpc错误代码转换为Twirp错误代码
func (e ErrorCode) ToTwirp() twirp.ErrorCode {
	switch e {
	case OK:
		return twirp.NoError // 没有错误
	case Canceled:
		return twirp.Canceled // 取消
	case Unknown:
		return twirp.Unknown // 未知
	case InvalidArgument:
		return twirp.InvalidArgument // 无效参数
	case MalformedRequest, MalformedResponse:
		return twirp.Malformed // 格式错误
	case DeadlineExceeded:
		return twirp.DeadlineExceeded // 超时
	case NotFound:
		return twirp.NotFound // 未找到
	case AlreadyExists:
		return twirp.AlreadyExists // 已存在
	case PermissionDenied:
		return twirp.PermissionDenied // 权限拒绝
	case ResourceExhausted:
		return twirp.ResourceExhausted // 资源耗尽
	case FailedPrecondition:
		return twirp.FailedPrecondition // 预条件失败
	case Aborted:
		return twirp.Aborted // 中止
	case OutOfRange:
		return twirp.OutOfRange // 超出范围
	case Unimplemented:
		return twirp.Unimplemented // 未实现
	case Internal:
		return twirp.Internal // 内部
	case Unavailable:
		return twirp.Unavailable // 不可用
	case DataLoss:
		return twirp.DataLoss // 数据丢失
	case Unauthenticated:
		return twirp.Unauthenticated // 未认证
	default:
		return twirp.Unknown // 未知
	}
}

// NewError 创建一个psrpc错误
func NewError(code ErrorCode, err error, details ...proto.Message) Error {
	if err == nil {
		panic("error is nil")
	}
	var protoDetails []*anypb.Any
	for _, e := range details {
		if p, err := anypb.New(e); err == nil {
			protoDetails = append(protoDetails, p)
		}
	}
	return &psrpcError{
		error:   err,
		code:    code,
		details: protoDetails,
	}
}

// NewErrorf 创建一个psrpc错误，使用格式化字符串
func NewErrorf(code ErrorCode, msg string, args ...interface{}) Error {
	return &psrpcError{
		error: fmt.Errorf(msg, args...),
		code:  code,
	}
}

// NewErrorFromResponse 从HTTP响应创建一个psrpc错误
func NewErrorFromResponse(code, err string, details ...*anypb.Any) Error {
	if code == "" {
		code = string(Unknown)
	}

	return &psrpcError{
		error:   errors.New(err),
		code:    ErrorCode(code),
		details: details,
	}
}

// 定义错误代码常量
const (
	OK ErrorCode = ""

	// Request Canceled by client 客户端请求取消
	Canceled ErrorCode = "canceled"
	// Could not unmarshal request 无法反序列化请求
	MalformedRequest ErrorCode = "malformed_request"
	// Could not unmarshal result 无法反序列化结果
	MalformedResponse ErrorCode = "malformed_result"
	// Request timed out 请求超时
	DeadlineExceeded ErrorCode = "deadline_exceeded"
	// Service unavailable due to load and/or affinity constraints 由于负载和/或亲和力约束，服务不可用
	Unavailable ErrorCode = "unavailable"
	// Unknown (server returned non-psrpc error) 未知（服务器返回非psrpc错误）
	Unknown ErrorCode = "unknown"

	// Invalid argument in request 请求中的无效参数
	InvalidArgument ErrorCode = "invalid_argument"
	// Entity not found 实体未找到
	NotFound ErrorCode = "not_found"
	// Cannot produce and entity matching requested format 无法生成与请求格式匹配的实体
	NotAcceptable ErrorCode = "not_acceptable"
	// Duplicate creation attempted 重复创建尝试
	AlreadyExists ErrorCode = "already_exists"
	// Caller does not have required permissions 调用者没有必要的权限
	PermissionDenied ErrorCode = "permission_denied"
	// Some resource has been exhausted, e.g. memory or quota 某些资源已经耗尽，例如内存或配额
	ResourceExhausted ErrorCode = "resource_exhausted"
	// Inconsistent state to carry out request 无法执行请求
	FailedPrecondition ErrorCode = "failed_precondition"
	// Request aborted 请求被中止
	Aborted ErrorCode = "aborted"
	// Operation was out of range 操作超出范围
	OutOfRange ErrorCode = "out_of_range"
	// Operation is not implemented by the server 服务器不支持该操作
	Unimplemented ErrorCode = "unimplemented"
	// Operation failed due to an internal error 操作由于内部错误而失败
	Internal ErrorCode = "internal"
	// Irrecoverable loss or corruption of data 不可恢复的数据丢失或损坏
	DataLoss ErrorCode = "data_loss"
	// Similar to PermissionDenied, used when the caller is unidentified 当调用者未识别时使用，类似于PermissionDenied
	Unauthenticated ErrorCode = "unauthenticated"
)

// psrpcError 是psrpc错误类型
type psrpcError struct {
	error                // 实现error接口
	code    ErrorCode    // 错误代码
	details []*anypb.Any // 错误详情(protobuf格式)
}

// Code 返回错误代码
func (e psrpcError) Code() ErrorCode {
	return e.code
}

// ToHttp 返回HTTP状态码
func (e psrpcError) ToHttp() int {
	return e.code.ToHTTP()
}

// DetailsProto 返回错误详情(protobuf格式)
func (e psrpcError) DetailsProto() []*anypb.Any {
	return e.details
}

// Details 返回错误详情(protobuf格式)
func (e psrpcError) Details() []any {
	return e.GRPCStatus().Details()
}

// GRPCStatus 返回GRPC状态
func (e psrpcError) GRPCStatus() *status.Status {
	return status.FromProto(&spb.Status{
		Code:    int32(e.code.ToGRPC()),
		Message: e.Error(),
		Details: e.details,
	})
}

// toTwirp 返回Twirp错误
func (e psrpcError) toTwirp() twirp.Error {
	return twirp.NewError(e.code.ToTwirp(), e.Error())
}

// As 检查错误是否可以转换为指定类型
func (e psrpcError) As(target any) bool {
	switch te := target.(type) {
	case *twirp.Error:
		*te = e.toTwirp()
		return true
	}

	return false
}

// Unwrap 返回错误链
func (e psrpcError) Unwrap() []error {
	return []error{e.error, e.code}
}
