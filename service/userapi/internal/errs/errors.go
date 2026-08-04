// Package errs 定义业务错误类型(携带 HTTP 状态码)。
package errs

// BizError 业务错误, Code 为 HTTP 状态码
type BizError struct {
	Code int
	Msg  string
}

func (e *BizError) Error() string { return e.Msg }

// New 构造业务错误
func New(code int, msg string) error {
	return &BizError{Code: code, Msg: msg}
}
