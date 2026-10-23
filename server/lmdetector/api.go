// Package lmdetector 是 Ikaleio/lm-detector 的 Go 移植，用于把「模型生成的随机整数序列」
// 归因到参考库里的具体模型。
//
// 参考库（shared_detector.bin / models.json）与算法均来自上游，MIT 许可，见 data/lm-detector/LICENSE。
// 原始实现：https://github.com/Ikaleio/lm-detector
// 移植来源：haowang02/cpa-plugin-codex-candy-eval（internal/plugin/fingerprint_detector.go）
package lmdetector

// Output 是一次数字生成挑战的回答。
type Output = fingerprintOutput

// Attribution 是归因结果：排名、概率、以及每条回答的诊断信息。
type Attribution = fingerprintAttribution

// Candidate 是排名里的一个候选模型。
type Candidate = fingerprintCandidate

// Analyze 用内置参考库对若干组回答做模型归因。
// 三组回答都有效时给出概率；只有一两组有效时只给排名；回答不像语言模型时 decision = not_model。
func Analyze(outputs []Output) (Attribution, error) {
	return analyzeFingerprint(outputs)
}

// Revision 返回参考库的 sha256，便于把结果和参考库版本对上。
func Revision() string {
	return detectorRevision
}
