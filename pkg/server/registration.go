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

import "reflect"

// Registerer 定义了注册和注销方法
type Registerer struct {
	register   any
	deregister any
}

// NewRegisterer 创建一个Registerer
func NewRegisterer(register, deregister any) Registerer {
	return Registerer{register, deregister}
}

// anySliceReflectValues 将any切片转换为reflect.Value切片
func anySliceReflectValues(anys []any) []reflect.Value {
	vals := make([]reflect.Value, len(anys))
	for i, a := range anys {
		vals[i] = reflect.ValueOf(a)
	}
	return vals
}

// RegistererSlice 是一个Registerer切片
type RegistererSlice []Registerer

// Register 注册
func (rs RegistererSlice) Register(params ...any) error {
	paramVals := anySliceReflectValues(params)
	for i, r := range rs {
		ret := reflect.ValueOf(r.register).Call(paramVals)
		if !ret[0].IsNil() {
			rs[:i].Deregister(params...)
			return ret[0].Interface().(error)
		}
	}
	return nil
}

// Deregister 注销
func (rs RegistererSlice) Deregister(params ...any) {
	paramVals := anySliceReflectValues(params)
	for _, r := range rs {
		reflect.ValueOf(r.deregister).Call(paramVals)
	}
}
