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

package bus

// EmptySubscription 空订阅
type EmptySubscription[MessageType any] struct{}

// Channel 返回空通道
func (s EmptySubscription[MessageType]) Channel() <-chan MessageType {
	return nil
}

// Close 关闭空订阅（什么也不做）
func (s EmptySubscription[MessageType]) Close() error {
	return nil
}
