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

package info

import (
	"sync"
	"unicode"

	"github.com/livekit/psrpc/internal/bus"
)

const lowerHex = "0123456789abcdef"

var channelChar = &unicode.RangeTable{
	R16: []unicode.Range16{
		{0x0030, 0x0039, 1}, // 0-9
		{0x0041, 0x005a, 1}, // A-Z
		{0x005f, 0x005f, 1}, // _
		{0x0061, 0x007a, 1}, // a-z
	},
	LatinOffset: 4,
}

func GetClaimRequestChannel(service, clientID string) bus.Channel {
	return bus.Channel{
		Legacy: formatChannel('|', service, clientID, "CLAIM"),
		Server: formatClientChannel(service, clientID, "CLAIM"),
	}
}

func GetStreamChannel(service, nodeID string) bus.Channel {
	return bus.Channel{
		Legacy: formatChannel('|', service, nodeID, "STR"),
		Server: formatClientChannel(service, nodeID, "STR"),
	}
}

// GetResponseChannel 获取响应通道
func GetResponseChannel(service, clientID string) bus.Channel {
	return bus.Channel{
		Legacy: formatChannel('|', service, clientID, "RES"),  // 格式：服务名称|客户端ID|RES
		Server: formatClientChannel(service, clientID, "RES"), // 格式：CLI.服务名称.客户端ID.RES
	}
}

func (i *RequestInfo) GetRPCChannel() bus.Channel {
	return bus.Channel{
		Legacy: formatChannel('|', i.Service, i.Method, i.Topic, "REQ"),
		Server: formatServerChannel(i.Service, i.Topic, i.Queue),
		Local:  formatLocalChannel(i.Method, "REQ"),
	}
}

// GetHandlerKey 获取方法的key
// 格式：方法名称.主题
// [Method].[Topic]
func (i *RequestInfo) GetHandlerKey() string {
	return formatChannel('.', i.Method, i.Topic)
}

func (i *RequestInfo) GetClaimResponseChannel() bus.Channel {
	return bus.Channel{
		Legacy: formatChannel('|', i.Service, i.Method, i.Topic, "RCLAIM"),
		Server: formatServerChannel(i.Service, i.Topic, false),
		Local:  formatLocalChannel(i.Method, "RCLAIM"),
	}
}

func (i *RequestInfo) GetStreamServerChannel() bus.Channel {
	return bus.Channel{
		Legacy: formatChannel('|', i.Service, i.Method, i.Topic, "STR"),
		Server: formatServerChannel(i.Service, i.Topic, false),
		Local:  formatLocalChannel(i.Method, "STR"),
	}
}

var scratch = &sync.Pool{
	New: func() any {
		b := make([]byte, 0, 512)
		return &b
	},
}

// formatClientChannel 格式化客户端通道
// 格式：CLI.服务名称.客户端ID.通道名称
func formatClientChannel(service, clientID, channel string) string {
	p := scratch.Get().(*[]byte)
	defer scratch.Put(p)
	b := append(*p, "CLI."...)
	b = append(b, service...)
	b = append(b, '.')
	b = append(b, clientID...)
	b = append(b, '.')
	b = append(b, channel...)
	return string(b)
}

func formatLocalChannel(method, channel string) string {
	p := scratch.Get().(*[]byte)
	defer scratch.Put(p)
	b := append(*p, method...)
	b = append(b, '.')
	b = append(b, channel...)
	return string(b)
}

func formatServerChannel(service string, topic []string, queue bool) string {
	p := scratch.Get().(*[]byte)
	defer scratch.Put(p)
	b := append(*p, "SRV."...)
	b = append(b, service...)
	for _, t := range topic {
		if len(t) != 0 {
			b = append(b, '.')
			b = appendSanitizedChannelPart(b, t)
		}
	}
	if queue {
		b = append(b, ".Q"...)
	}
	return string(b)
}

// formatChannel 将字符串或字符串数组转换成合法的通道名称，并添加分隔符
func formatChannel(delim byte, parts ...any) string {
	p := scratch.Get().(*[]byte)
	defer scratch.Put(p)
	return string(appendChannelParts(*p, delim, parts...))
}

// appendChannelParts 将字符串或字符串数组转换成合法的通道名称，并添加分隔符
func appendChannelParts[T any](buf []byte, delim byte, parts ...T) []byte {
	var prefix bool
	for _, t := range parts {
		if prefix {
			buf = append(buf, delim)
		}
		l := len(buf)
		switch v := any(t).(type) {
		case string:
			buf = appendSanitizedChannelPart(buf, v)
		case []string:
			buf = appendChannelParts(buf, delim, v...)
		}
		prefix = len(buf) > l
	}
	return buf
}

// 将字符串转换为合法的通道名称
// 合法的通道名称只能包含0-9, A-Z, a-z, _
// 其它字符转换为小写的utf-8编码的字符串 如果 #世界 => u+0023u+4e16u+754c
// 适合多语言环境下使用更为标准通用
func appendSanitizedChannelPart(buf []byte, s string) []byte {
	for _, r := range s {
		if unicode.Is(channelChar, r) {
			buf = append(buf, byte(r))
		} else if r < 0x10000 {
			buf = append(buf, `u+`...)
			for s := 12; s >= 0; s -= 4 {
				buf = append(buf, lowerHex[r>>uint(s)&0xF])
			}
		} else {
			buf = append(buf, `U+`...)
			for s := 28; s >= 0; s -= 4 {
				buf = append(buf, lowerHex[r>>uint(s)&0xF])
			}
		}
	}
	return buf
}
