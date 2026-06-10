package route

import (
	"crypto/ecdsa"
	"log"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/NimoTech/NimoOS/codegen"
	"github.com/NimoTech/NimoOS/pkg/config"
	"github.com/NimoTech/NimoOS/pkg/utils/file"

	"github.com/NimoTech/NimoOS-Common/external"
	"github.com/NimoTech/NimoOS-Common/utils/jwt"
	v2Route "github.com/NimoTech/NimoOS/route/v2"
	"github.com/deepmap/oapi-codegen/pkg/middleware"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/labstack/echo/v4"
	echo_middleware "github.com/labstack/echo/v4/middleware"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"go.uber.org/zap"
)

var (
	_swagger *openapi3.T

	V2APIPath  string
	V2DocPath  string
	V3FilePath string
)

func init() {
	swagger, err := codegen.GetSwagger()
	if err != nil {
		panic(err)
	}

	_swagger = swagger

	V2APIPath = "/v2/nimoos"
	V2DocPath = "/doc" + V2APIPath
	V3FilePath = "/v3/file"
}

func InitV2Router() http.Handler {
	appManagement := v2Route.NewNimoOS()

	e := echo.New()

	e.Use((echo_middleware.CORSWithConfig(echo_middleware.CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{echo.POST, echo.GET, echo.OPTIONS, echo.PUT, echo.DELETE},
		AllowHeaders:     []string{echo.HeaderAuthorization, echo.HeaderContentLength, echo.HeaderXCSRFToken, echo.HeaderContentType, echo.HeaderAccessControlAllowOrigin, echo.HeaderAccessControlAllowHeaders, echo.HeaderAccessControlAllowMethods, echo.HeaderConnection, echo.HeaderOrigin, echo.HeaderXRequestedWith},
		ExposeHeaders:    []string{echo.HeaderContentLength, echo.HeaderAccessControlAllowOrigin, echo.HeaderAccessControlAllowHeaders},
		MaxAge:           172800,
		AllowCredentials: true,
	})))

	e.Use(echo_middleware.Gzip())
	e.Use(echo_middleware.Recover())

	e.Use(echo_middleware.Logger())

	e.Use(echo_middleware.JWTWithConfig(echo_middleware.JWTConfig{
		Skipper: func(c echo.Context) bool {
			// If there's an auth token, we MUST parse it to get the user identity,
			// even if the request comes from localhost. Only skip if it's a true
			// internal call with no auth info.
			hasAuth := len(c.Request().Header.Get(echo.HeaderAuthorization)) > 0 ||
				len(c.QueryParam("token")) > 0
			if hasAuth {
				return false
			}
			return c.RealIP() == "::1" || c.RealIP() == "127.0.0.1"
		},
		ParseTokenFunc: func(token string, c echo.Context) (interface{}, error) {
			valid, claims, err := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(config.CommonInfo.RuntimePath) })
			if err != nil || !valid {
				return nil, echo.ErrUnauthorized
			}
			c.Request().Header.Set("user_id", strconv.Itoa(claims.ID))

			return claims, nil
		},
		TokenLookupFuncs: []echo_middleware.ValuesExtractor{
			func(ctx echo.Context) ([]string, error) {
				if len(ctx.Request().Header.Get(echo.HeaderAuthorization)) > 0 {
					return []string{ctx.Request().Header.Get(echo.HeaderAuthorization)}, nil
				}
				return []string{ctx.QueryParam("token")}, nil
			},
		},
	}))

	// e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
	// 	return func(c echo.Context) error {
	// 		switch c.Request().Header.Get(echo.HeaderContentType) {
	// 		case common.MIMEApplicationYAML: // in case request contains a compose content in YAML
	// 			return middleware.OapiRequestValidatorWithOptions(_swagger, &middleware.Options{
	// 				Options: openapi3filter.Options{
	// 					AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
	// 					// ExcludeRequestBody:  true,
	// 					// ExcludeResponseBody: true,
	// 				},
	// 			})(next)(c)

	// 		default:
	// 			return middleware.OapiRequestValidatorWithOptions(_swagger, &middleware.Options{
	// 				Options: openapi3filter.Options{
	// 					AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
	// 				},
	// 			})(next)(c)
	// 		}
	// 	}
	// })

	e.Use(middleware.OapiRequestValidatorWithOptions(_swagger, &middleware.Options{
		Skipper: func(c echo.Context) bool {
			// jump validate when upload file
			// because file upload can't pass validate
			// issue: https://github.com/deepmap/oapi-codegen/issues/514
			if strings.Contains(c.Request().Header.Get(echo.HeaderContentType), "multipart/form-data") {
				return true
			}
			// Skip validation for local_storage routes since they are manually registered and missing from embedded swagger
			if strings.Contains(c.Request().URL.Path, "/local_storage/") {
				return true
			}
			// tus 上传端点不在 OpenAPI 规格里，跳过校验。
			if strings.Contains(c.Request().URL.Path, "/file/upload-tus") {
				return true
			}
			// upload-precheck 端点不在 OpenAPI 规格里，跳过校验。
			if strings.Contains(c.Request().URL.Path, "/file/upload-precheck") {
				return true
			}
			return false
		},
		Options: openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}))

	codegen.RegisterHandlersWithBaseURL(e, appManagement, V2APIPath)

	// Manually register missing routes using type assertion to bypass truncated codegen interface
	if si, ok := appManagement.(interface {
		GetLocalStorageDisplayNames(echo.Context) error
		UpdateLocalStorageDisplayName(echo.Context) error
	}); ok {
		e.GET(V2APIPath+"/local_storage/display_names", si.GetLocalStorageDisplayNames)
		e.PUT(V2APIPath+"/local_storage/display_name", si.UpdateLocalStorageDisplayName)
	}

	if tusH, terr := v2Route.NewFileTUSHandler(); terr != nil {
		logger.Error("Files tus handler init failed", zap.Error(terr))
	} else {
		e.Any(V2APIPath+"/file/upload-tus", echo.WrapHandler(tusH))
		e.Any(V2APIPath+"/file/upload-tus/*", echo.WrapHandler(tusH))
	}

	e.POST(V2APIPath+"/file/upload-precheck", v2Route.FileUploadPrecheck)

	e.Any("/v2/nimoos/testecho", func(c echo.Context) error {
		return c.String(200, "echo works at "+c.Request().URL.Path)
	})

	for _, route := range e.Routes() {
		logger.Info("Registered V2 Route", zap.String("method", route.Method), zap.String("path", route.Path))
	}

	return e
}

func InitV2DocRouter(docHTML string, docYAML string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == V2DocPath {
			if _, err := w.Write([]byte(docHTML)); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}

		if r.URL.Path == V2DocPath+"/openapi.yaml" {
			if _, err := w.Write([]byte(docYAML)); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
			}
		}
	})
}

func InitFile() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if len(token) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message": "token not found"}`))
			return
		}

		valid, _, errs := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(config.CommonInfo.RuntimePath) })
		if errs != nil || !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message": "validation failure"}`))
			return
		}
		filePath := r.URL.Query().Get("path")
		fileName := path.Base(filePath)
		// Default to attachment (download). inline=1 lets the browser render the
		// file in-tab (PDF/image preview, + #page=N navigation via Range support).
		disposition := "attachment"
		if r.URL.Query().Get("inline") == "1" {
			disposition = "inline"
		}
		w.Header().Add("Content-Disposition", disposition+"; filename*=utf-8''"+url.PathEscape(fileName))
		http.ServeFile(w, r, filePath)
	})
}

func InitDir() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if len(token) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message": "token not found"}`))
			return
		}

		valid, _, errs := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(config.CommonInfo.RuntimePath) })
		if errs != nil || !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message": "validation failure"}`))
			return
		}
		t := r.URL.Query().Get("format")
		files := r.URL.Query().Get("files")

		if len(files) == 0 {
			// w.JSON(common_err.CLIENT_ERROR, model.Result{
			// 	Success: common_err.INVALID_PARAMS,
			// 	Message: common_err.GetMsg(common_err.INVALID_PARAMS),
			// })
			return
		}
		list := strings.Split(files, ",")
		for _, v := range list {
			if !file.Exists(v) {
				// return ctx.JSON(common_err.SERVICE_ERROR, model.Result{
				// 	Success: common_err.FILE_DOES_NOT_EXIST,
				// 	Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST),
				// })
				return
			}
		}
		w.Header().Add("Content-Type", "application/octet-stream")
		w.Header().Add("Content-Transfer-Encoding", "binary")
		w.Header().Add("Cache-Control", "no-cache")
		// handles only single files not folders and multiple files
		//		if len(list) == 1 {

		// filePath := list[0]
		//			info, err := os.Stat(filePath)
		//			if err != nil {

		// w.JSON(http.StatusOK, model.Result{
		// 	Success: common_err.FILE_DOES_NOT_EXIST,
		// 	Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST),
		// })
		//return
		//			}
		//}

		extension, ar, err := file.GetCompressionAlgorithm(t)
		if err != nil {
			// w.JSON(common_err.CLIENT_ERROR, model.Result{
			// 	Success: common_err.INVALID_PARAMS,
			// 	Message: common_err.GetMsg(common_err.INVALID_PARAMS),
			// })
			return
		}

		err = ar.Create(w)
		if err != nil {
			//  return ctx.JSON(common_err.SERVICE_ERROR, model.Result{
			// 	Success: common_err.SERVICE_ERROR,
			// 	Message: common_err.GetMsg(common_err.SERVICE_ERROR),
			// 	Data:    err.Error(),
			// })
			return
		}
		defer ar.Close()
		commonDir := file.CommonPrefix(filepath.Separator, list...)

		currentPath := filepath.Base(commonDir)

		name := "_" + currentPath
		name += extension
		w.Header().Add("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(name))
		for _, fname := range list {
			err = file.AddFile(ar, fname, commonDir)
			if err != nil {
				log.Printf("Failed to archive %s: %v", fname, err)
			}
		}
	})
}
