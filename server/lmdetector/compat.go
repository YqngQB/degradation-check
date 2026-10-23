package lmdetector

import "encoding/json"

// fingerprintTarget 是一次归因期望的回答条数，与上游 fingerprintTarget 一致。
const fingerprintTarget = 3

// mustDecode 把参考库的 JSON 解成值；格式不对就直接 panic（移植来源同款行为，
// 参考库是编译期嵌入的常量，解析失败说明包被破坏）。
func mustDecode[T any](data []byte) T {
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		panic(err)
	}
	return value
}
