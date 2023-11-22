package sunerror

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const burSize int = 3000

// SunError 自定义Error类型(实现了go内嵌error接口)
// 1. 包含三元组(Code + Msg + Status)
// 2. 自动打印日志, NewSunError时打印
// 3. 堆栈信息
type SunError struct {
	code        string
	msg         string
	status      string
	level       SunErrLevel
	detail      string // 单号等打印的补充信息
	fnName      string
	storeStack  bool
	stack       []byte
	stackRows   int
	depth       int
	channelCode string                                        // 下游错误码
	channelMsg  string                                        // 下游错误信息
	asyncFn     func(ctx context.Context, sunError *SunError) // 异步执行函数
	logEngine   logFunc                                       // 用户自定义的日志引擎
}

// SunErrLevel 错误等级, 会影响日志打印时的level
type SunErrLevel int8

// SunErrOption SunError属性设置函数
type SunErrOption func(sunError *SunError)

const (
	// InfoLevel Info级别
	InfoLevel SunErrLevel = iota
	// WarnLevel Warn级别
	WarnLevel
	// ErrorLevel Error级别
	ErrorLevel
)

func (e SunError) Error() string {
	errInfo := fmt.Sprintf("[%s] code=%s, msg=%s, channelCode=%s, channelMsg=%s, detail=%s",
		e.fnName, e.code, e.msg, e.channelCode, e.channelMsg, e.detail)
	if e.storeStack {
		errInfo = errInfo + "\n" + string(e.stack)
	}
	return errInfo
}

func (e SunError) GetCode() string {
	return e.code
}

func (e SunError) GetStatus() string {
	return e.status
}

func (e SunError) GetMsg() string {
	return e.msg
}
func (e SunError) GetDetail() string {
	return e.detail
}

func (e SunError) GetChannelCode() string {
	return e.channelCode
}

func (e SunError) GetChannelMsg() string {
	return e.channelMsg
}

func NewSunError(ctx context.Context, code, status, msg string, opts ...SunErrOption) *SunError {
	sunErr := &SunError{
		code:       code,
		msg:        msg,
		status:     status,
		level:      ErrorLevel,
		storeStack: true,
		depth:      2,
		stackRows:  10,
	}
	for _, opt := range opts {
		opt(sunErr)
	}

	if len(sunErr.fnName) == 0 {
		sunErr.fnName = getCurrentFunc(sunErr.depth)
	}

	if sunErr.storeStack {
		sunErr.stack = getStack(sunErr.depth, sunErr.stackRows)
	}

	sunErr.ctxLog(ctx)

	if sunErr.asyncFn != nil {
		sunErr.safeGo(ctx, func() {
			sunErr.asyncFn(ctx, sunErr)
		})
	}
	return sunErr
}

// 异步执行并在发生panic后recover&打印堆栈
func (e SunError) safeGo(ctx context.Context, f func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, burSize)
				buf = buf[:runtime.Stack(buf, false)]
				e.logEngine(ctx, "SafeGo has panic:%s", string(buf))
			}
		}()
		f()
	}()
}

// WithLogEngine 自定义的日志引擎 required
func WithLogEngine(log logFunc) SunErrOption {
	return func(e *SunError) {
		e.logEngine = log
	}
}

// WithLogLevelOption 设置日志打印等级, 不设置时默认为ErrorLevel
func WithLogLevelOption(level SunErrLevel) SunErrOption {
	return func(e *SunError) {
		e.level = level
	}
}

// WithDetailOption 设置报错详细信息, 如单号/Uid等参数
func WithDetailOption(format string, v ...interface{}) SunErrOption {
	return func(e *SunError) {
		e.detail = fmt.Sprintf(format, v...)
	}
}

// WithFuncNameOption 设置打印日志时的报错函数名, 不设置时默认打印调用NewBizError的函数名
func WithFuncNameOption(funcName string) SunErrOption {
	return func(e *SunError) {
		e.fnName = funcName
	}
}

// WithStackOption 设置是否保存函数栈信息, 不设置时默认保存
func WithStackOption(storeStack bool) SunErrOption {
	return func(e *SunError) {
		e.storeStack = storeStack
	}
}

// WithSkipDepthOption 设置跳过的函数栈深度, 当你封装NewBizError时应该设置
func WithSkipDepthOption(skipDepth int) SunErrOption {
	return func(e *SunError) {
		e.depth += skipDepth
	}
}

// WithChannelRespOption 设置下游返回的错误码/消息, 当异常是下游导致的可以设置
func WithChannelRespOption(channelCode, channelMsg string) SunErrOption {
	return func(e *SunError) {
		e.channelCode = channelCode
		e.channelMsg = channelMsg
	}
}

// WithAsyncExecutor 产生错误后异步执行器, 如进行上报metrics打点
func WithAsyncExecutor(fn func(context.Context, *SunError)) SunErrOption {
	return func(e *SunError) {
		e.asyncFn = fn
	}
}

// WithStackRows 函数堆栈保存的行数, 默认保存10行
func WithStackRows(stackRows int) SunErrOption {
	return func(e *SunError) {
		if stackRows > 0 {
			e.stackRows = stackRows
		}
	}
}

type logFunc func(ctx context.Context, format string, v ...interface{})

func (e SunError) ctxLog(ctx context.Context) {
	e.getLogFunc()(ctx, "%s", e.Error())
}

func (e SunError) getLogFunc() logFunc {
	switch e.level {
	case InfoLevel:
		return e.logEngine
	case WarnLevel:
		return e.logEngine
	case ErrorLevel:
		return e.logEngine
	}
	return e.logEngine
}

func getCurrentFunc(skip int) string {
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "??:0:??()"
	}
	funcName := runtime.FuncForPC(pc).Name()
	funcName = strings.TrimLeft(filepath.Ext(funcName), ".") + "()"
	return filepath.Base(file) + ":" + strconv.Itoa(line) + ":" + funcName
}

func getStack(skip, rows int) []byte {
	buf := new(bytes.Buffer)
	for i := skip; i-skip < rows; i++ {
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		fmt.Fprintf(buf, "%s:%d (0x%x)\n", file, line, pc)
	}
	return buf.Bytes()
}
