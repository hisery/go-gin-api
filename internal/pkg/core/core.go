package core

import (
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"time"

	_ "github.com/xinliangnote/go-gin-api/docs"
	"github.com/xinliangnote/go-gin-api/internal/pkg/errno"
	"github.com/xinliangnote/go-gin-api/internal/pkg/journal"
	"github.com/xinliangnote/go-gin-api/pkg/color"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	cors "github.com/rs/cors/wrapper/gin"
	ginSwagger "github.com/swaggo/gin-swagger"
	"github.com/swaggo/gin-swagger/swaggerFiles"
	"go.uber.org/multierr"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

const _UI = `
 ██████╗  ██████╗        ██████╗ ██╗███╗   ██╗       █████╗ ██████╗ ██╗
██╔════╝ ██╔═══██╗      ██╔════╝ ██║████╗  ██║      ██╔══██╗██╔══██╗██║
██║  ███╗██║   ██║█████╗██║  ███╗██║██╔██╗ ██║█████╗███████║██████╔╝██║
██║   ██║██║   ██║╚════╝██║   ██║██║██║╚██╗██║╚════╝██╔══██║██╔═══╝ ██║
╚██████╔╝╚██████╔╝      ╚██████╔╝██║██║ ╚████║      ██║  ██║██║     ██║
 ╚═════╝  ╚═════╝        ╚═════╝ ╚═╝╚═╝  ╚═══╝      ╚═╝  ╚═╝╚═╝     ╚═╝
`

const _MaxBurstSize = 100

type Option func(*option)

type option struct {
	disablePProf      bool
	disableSwagger    bool
	disablePrometheus bool
	panicNotify       OnPanicNotify
	recordMetrics     RecordMetrics
	enableCors        bool
	enableRate        bool
}

// OnPanicNotify 发生panic时通知用
type OnPanicNotify func(ctx Context, err interface{}, stackInfo string)

// RecordMetrics 记录prometheus指标用
// 如果使用AliasForRecordMetrics配置了别名，uri将被替换为别名。
type RecordMetrics func(method, uri string, success bool, httpCode, businessCode int, costSeconds float64)

// WithDisablePProf 禁用 pprof
func WithDisablePProf() Option {
	return func(opt *option) {
		opt.disablePProf = true
	}
}

// WithDisableSwagger 禁用 swagger
func WithDisableSwagger() Option {
	return func(opt *option) {
		opt.disableSwagger = true
	}
}

// WithDisableproPrometheus 禁用prometheus
func WithDisableproPrometheus() Option {
	return func(opt *option) {
		opt.disablePrometheus = true
	}
}

// WithPanicNotify 设置panic时的通知回调
func WithPanicNotify(notify OnPanicNotify) Option {
	return func(opt *option) {
		opt.panicNotify = notify
		fmt.Println(color.Green("* [register panic notify]"))
	}
}

// WithRecordMetrics 设置记录prometheus记录指标回调
func WithRecordMetrics(record RecordMetrics) Option {
	return func(opt *option) {
		opt.recordMetrics = record
	}
}

// WithEnableCors 开启CORS
func WithEnableCors() Option {
	return func(opt *option) {
		opt.enableCors = true
		fmt.Println(color.Green("* [register cors]"))
	}
}

func WithEnableRate() Option {
	return func(opt *option) {
		opt.enableRate = true
		fmt.Println(color.Green("* [register rate]"))
	}
}

// DisableJournal 标识某些请求不记录journal
func DisableJournal(ctx Context) {
	ctx.disableJournal()
}

// AliasForRecordMetrics 对请求uri起个别名，用于prometheus记录指标。
// 如：Get /user/:username 这样的uri，因为username会有非常多的情况，这样记录prometheus指标会非常的不有好。
func AliasForRecordMetrics(path string) HandlerFunc {
	return func(ctx Context) {
		ctx.setAlias(path)
	}
}

// WrapAuthHandler 用来处理 Auth 的入口，在之后的handler中只需 ctx.UserID() ctx.UserName() 即可。
func WrapAuthHandler(handler func(Context) (userID int, userName string, err errno.Error)) HandlerFunc {
	return func(ctx Context) {
		userID, userName, err := handler(ctx)
		if err != nil {
			ctx.AbortWithError(err)
			return
		}
		ctx.setUserID(userID)
		ctx.setUserName(userName)
	}
}

// RouterGroup 包装gin的RouterGroup
type RouterGroup interface {
	Group(string, ...HandlerFunc) RouterGroup
	IRoutes
}

var _ IRoutes = (*router)(nil)

// IRoutes 包装gin的IRoutes
type IRoutes interface {
	Any(string, ...HandlerFunc)
	GET(string, ...HandlerFunc)
	POST(string, ...HandlerFunc)
	DELETE(string, ...HandlerFunc)
	PATCH(string, ...HandlerFunc)
	PUT(string, ...HandlerFunc)
	OPTIONS(string, ...HandlerFunc)
	HEAD(string, ...HandlerFunc)
}

type router struct {
	group *gin.RouterGroup
}

func (r *router) Group(relativePath string, handlers ...HandlerFunc) RouterGroup {
	group := r.group.Group(relativePath, wrapHandlers(handlers...)...)
	return &router{group: group}
}

func (r *router) Any(relativePath string, handlers ...HandlerFunc) {
	r.group.Any(relativePath, wrapHandlers(handlers...)...)
}

func (r *router) GET(relativePath string, handlers ...HandlerFunc) {
	r.group.GET(relativePath, wrapHandlers(handlers...)...)
}

func (r *router) POST(relativePath string, handlers ...HandlerFunc) {
	r.group.POST(relativePath, wrapHandlers(handlers...)...)
}

func (r *router) DELETE(relativePath string, handlers ...HandlerFunc) {
	r.group.DELETE(relativePath, wrapHandlers(handlers...)...)
}

func (r *router) PATCH(relativePath string, handlers ...HandlerFunc) {
	r.group.PATCH(relativePath, wrapHandlers(handlers...)...)
}

func (r *router) PUT(relativePath string, handlers ...HandlerFunc) {
	r.group.PUT(relativePath, wrapHandlers(handlers...)...)
}

func (r *router) OPTIONS(relativePath string, handlers ...HandlerFunc) {
	r.group.OPTIONS(relativePath, wrapHandlers(handlers...)...)
}

func (r *router) HEAD(relativePath string, handlers ...HandlerFunc) {
	r.group.HEAD(relativePath, wrapHandlers(handlers...)...)
}

func wrapHandlers(handlers ...HandlerFunc) []gin.HandlerFunc {
	funcs := make([]gin.HandlerFunc, len(handlers))
	for i, handler := range handlers {
		handler := handler
		funcs[i] = func(c *gin.Context) {
			ctx := newContext(c)
			defer releaseContext(ctx)

			handler(ctx)
		}
	}

	return funcs
}

var _ Mux = (*mux)(nil)

// Mux http mux
type Mux interface {
	http.Handler
	Group(relativePath string, handlers ...HandlerFunc) RouterGroup
}

type mux struct {
	engine *gin.Engine
}

func (m *mux) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	m.engine.ServeHTTP(w, req)
}

func (m *mux) Group(relativePath string, handlers ...HandlerFunc) RouterGroup {
	return &router{
		group: m.engine.Group(relativePath, wrapHandlers(handlers...)...),
	}
}

func New(logger *zap.Logger, options ...Option) (Mux, error) {
	if logger == nil {
		return nil, errors.New("logger required")
	}

	gin.SetMode(gin.ReleaseMode)
	gin.DisableBindValidation()
	mux := &mux{
		engine: gin.New(),
	}

	fmt.Println(color.Blue(_UI))

	// withoutLogPaths 这些请求，默认不记录日志
	withoutJournalPaths := map[string]bool{
		"/metrics": true,

		"/debug/pprof/":             true,
		"/debug/pprof/cmdline":      true,
		"/debug/pprof/profile":      true,
		"/debug/pprof/symbol":       true,
		"/debug/pprof/trace":        true,
		"/debug/pprof/allocs":       true,
		"/debug/pprof/block":        true,
		"/debug/pprof/goroutine":    true,
		"/debug/pprof/heap":         true,
		"/debug/pprof/mutex":        true,
		"/debug/pprof/threadcreate": true,

		"/favicon.ico": true,
	}

	opt := new(option)
	for _, f := range options {
		f(opt)
	}

	if !opt.disablePProf {
		pprof.Register(mux.engine) // register pprof to gin
		fmt.Println(color.Green("* [register pprof]"))
	}

	if !opt.disableSwagger {
		mux.engine.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler)) // register swagger
		fmt.Println(color.Green("* [register swagger]"))
	}

	if !opt.disablePrometheus {
		mux.engine.GET("/metrics", gin.WrapH(promhttp.Handler())) // register prometheus
		fmt.Println(color.Green("* [register prometheus]"))
	}

	if opt.enableCors {
		mux.engine.Use(cors.New(cors.Options{
			AllowedOrigins: []string{"*"},
			AllowedMethods: []string{
				http.MethodHead,
				http.MethodGet,
				http.MethodPost,
				http.MethodPut,
				http.MethodPatch,
				http.MethodDelete,
			},
			AllowedHeaders:     []string{"*"},
			AllowCredentials:   true,
			OptionsPassthrough: true,
		}))
	}

	// recover两次，防止处理时发生panic，尤其是在OnPanicNotify中。
	mux.engine.Use(func(ctx *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				logger.Error("got panic", zap.String("panic", fmt.Sprintf("%+v", err)), zap.String("stack", string(debug.Stack())))
			}
		}()

		ctx.Next()
	})

	mux.engine.Use(func(ctx *gin.Context) {
		ts := time.Now()

		context := newContext(ctx)
		defer releaseContext(context)

		context.init()
		context.setLogger(logger)

		if !withoutJournalPaths[ctx.Request.URL.Path] {
			if journalID := context.GetHeader(journal.JournalHeader); journalID != "" {
				context.setJournal(journal.NewJournal(journalID))
			} else {
				context.setJournal(journal.NewJournal(""))
			}
		}

		defer func() {
			if err := recover(); err != nil {
				stackInfo := string(debug.Stack())
				logger.Error("got panic", zap.String("panic", fmt.Sprintf("%+v", err)), zap.String("stack", stackInfo))
				context.SetPayload(errno.ErrServer)

				if notify := opt.panicNotify; notify != nil {
					notify(context, err, stackInfo)
				}
			}

			if ctx.Writer.Status() == http.StatusNotFound {
				return
			}

			var (
				response errno.Error
				abortErr error
			)

			if ctx.IsAborted() {
				for i := range ctx.Errors { // gin error
					multierr.AppendInto(&abortErr, ctx.Errors[i])
				}

				if err := context.abortError(); err != nil { // customer err
					multierr.AppendInto(&abortErr, errors.New(err.GetMsg()))
					response = err
				}
			} else {
				response = context.GetPayload()
			}

			if response != nil {
				if x := context.Journal(); x != nil {
					context.SetHeader(journal.JournalHeader, x.ID())
					response.WithID(x.ID())
				} else {
					response.WithID("")
				}
				ctx.JSON(http.StatusOK, response)
			}

			if opt.recordMetrics != nil {
				uri := context.URI()
				if alias := context.Alias(); alias != "" {
					uri = alias
				}

				businessCode := 0
				if response != nil {
					businessCode = response.GetCode()
				}

				opt.recordMetrics(context.Method(), uri, !ctx.IsAborted() && ctx.Writer.Status() == http.StatusOK, ctx.Writer.Status(), businessCode, time.Since(ts).Seconds())
			}

			var j *journal.Journal
			if x := context.Journal(); x != nil {
				j = x.(*journal.Journal)
			} else {
				return
			}

			decodedURL, _ := url.QueryUnescape(ctx.Request.URL.RequestURI())
			j.WithRequest(&journal.Request{
				TTL:        "un-limit",
				Method:     ctx.Request.Method,
				DecodedURL: decodedURL,
				Header:     ctx.Request.Header,
				Body:       string(context.RawData()),
			})

			j.WithResponse(&journal.Response{
				Header:     ctx.Writer.Header(),
				StatusCode: ctx.Writer.Status(),
				Status:     http.StatusText(ctx.Writer.Status()),
				Body:       response,
			})

			j.Success = !ctx.IsAborted() && ctx.Writer.Status() == http.StatusOK
			j.CostSeconds = time.Since(ts).Seconds()

			logger.Info("interceptor", zap.Any("journal", j))
		}()

		ctx.Next()
	})

	if opt.enableRate {
		limiter := rate.NewLimiter(rate.Every(time.Second*1), _MaxBurstSize)
		mux.engine.Use(func(ctx *gin.Context) {
			context := newContext(ctx)
			defer releaseContext(context)

			if !limiter.Allow() {
				context.SetPayload(errno.ErrManyRequest)
				ctx.Abort()
				return
			}
			ctx.Next()
		})
	}

	mux.engine.NoMethod(wrapHandlers(DisableJournal)...)
	mux.engine.NoRoute(wrapHandlers(DisableJournal)...)

	h := mux.Group("/h", DisableJournal)
	{
		h.GET("/ping", func(ctx Context) {
			ctx.SetPayload(errno.OK.WithData("pong"))
		})

		h.GET("/info", func(ctx Context) {
			resp := &struct {
				Header interface{} `json:"header"`
				Ts     time.Time   `json:"ts"`
			}{
				Header: ctx.Header(),
				Ts:     time.Now(),
			}
			ctx.SetPayload(errno.OK.WithData(resp))
		})
	}

	return mux, nil
}
