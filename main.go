//go:generate bash -c "mkdir -p codegen && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,server,spec -package codegen api/nimoos/openapi.yaml > codegen/nimoos_api.go"
//go:generate bash -c "mkdir -p codegen/message_bus && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,client -package message_bus ../NimoOS-MessageBus/api/message_bus/openapi.yaml > codegen/message_bus/api.go"
package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/NimoTech/NimoOS-Common/external"
	"github.com/NimoTech/NimoOS-Common/model"
	"github.com/NimoTech/NimoOS-Common/utils/command"
	"github.com/NimoTech/NimoOS-Common/utils/constants"
	"github.com/NimoTech/NimoOS-Common/utils/logger"

	util_http "github.com/NimoTech/NimoOS-Common/utils/http"

	"github.com/NimoTech/NimoOS/common"
	"github.com/NimoTech/NimoOS/pkg/cache"
	"github.com/NimoTech/NimoOS/pkg/config"
	"github.com/NimoTech/NimoOS/pkg/sqlite"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/NimoTech/NimoOS/route"
	"github.com/NimoTech/NimoOS/service"
	"github.com/coreos/go-systemd/daemon"
	"go.uber.org/zap"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

const LOCALHOST = "127.0.0.1"

var sqliteDB *gorm.DB

var (
	commit = "private build"
	date   = "private build"

	//go:embed api/index.html
	_docHTML string

	//go:embed api/nimoos/openapi.yaml
	_docYAML string

	//go:embed build/sysroot/etc/nimoos/nimoos.conf.sample
	_confSample string

	configFlag  = flag.String("c", "", "config address")
	dbFlag      = flag.String("db", "", "db path")
	versionFlag = flag.Bool("v", false, "version")
)

func init() {
	flag.Parse()
	
	// Create a log file to capture panics
	f, _ := os.OpenFile("/tmp/nimoos_panic.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	os.Stdout = f
	os.Stderr = f

	if *versionFlag {
		fmt.Println("v" + common.VERSION)
		return
	}

	println("git commit:", commit)
	println("build date:", date)

	config.InitSetup(*configFlag, _confSample)

	logger.LogInit(config.AppInfo.LogPath, config.AppInfo.LogSaveName, config.AppInfo.LogFileExt)
	if len(*dbFlag) == 0 {
		*dbFlag = config.AppInfo.DBPath + "/db"
	}

	sqliteDB = sqlite.GetDb(*dbFlag)
	// gredis.GetRedisConn(config.RedisInfo),

	// user.db lives alongside nimoOS.db; open it read-only for folder-permission lookups.
	userDBPath := config.AppInfo.DBPath + "/db/user.db"
	service.MyService = service.NewServiceWithUserDB(sqliteDB, config.CommonInfo.RuntimePath, userDBPath)

	service.Cache = cache.Init()

	service.GetCPUThermalZone()

	route.InitFunction()

	//service.MyService.System().GenreateSystemEntry()
	///
	//service.MountLists = make(map[string]*mountlib.MountPoint)
	//configfile.Install()
}

// @title nimoOS API
// @version 1.0.0
// @contact.name lauren.pan
// @contact.url https://www.zimaboard.com
// @contact.email lauren.pan@icewhale.org
// @description nimoOS v1版本api
// @host 192.168.2.217:8089
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name Authorization
// @BasePath /v1
func main() {
	if *versionFlag {
		return
	}
	v1Router := route.InitV1Router()

	v2Router := route.InitV2Router()
	v2DocRouter := route.InitV2DocRouter(_docHTML, _docYAML)
	v3File := route.InitFile()
	mux := &util_http.HandlerMultiplexer{
		HandlerMap: map[string]http.Handler{
			"v1":  v1Router,
			"v2":  v2Router,
			"v3":  v3File,
			"doc": v2DocRouter,
		},
	}

	// Start the Intel GPU monitor (no-op if intel_gpu_top is missing).
	external.StartIntelGpuMonitor()

	crontab := cron.New(cron.WithSeconds())
	if _, err := crontab.AddFunc("@every 5s", route.SendAllHardwareStatusBySocket); err != nil {
		logger.Error("add crontab error", zap.Error(err))
	}

	crontab.Start()
	defer crontab.Stop()

	listener, err := net.Listen("tcp", net.JoinHostPort(LOCALHOST, "0"))
	if err != nil {
		panic(err)
	}
	routers := []string{
		"/v1/sys",
		"/v1/port",
		"/v1/file",
		"/v1/folder",
		"/v1/batch",
		"/v1/image",
		"/v1/samba",
		"/v1/notify",
		"/v1/driver",
		"/v1/cloud",
		"/v1/recover",
		"/v1/other",
		"/v1/zt",
		"/v1/test",
		route.V2APIPath,
		route.V2DocPath,
		route.V3FilePath,
	}
	for _, apiPath := range routers {
		target := "http://" + listener.Addr().String()
		logger.Info("Registering route to Gateway", zap.String("path", apiPath), zap.String("target", target))
		err = service.MyService.Gateway().CreateRoute(&model.Route{
			Path:   apiPath,
			Target: target,
		})
		if err != nil {
			logger.Error("Failed to register route to Gateway", zap.String("path", apiPath), zap.Error(err))
			panic(err)
		}
	}

	// register at message bus
	for i := 0; i < 10; i++ {
		response, err := service.MyService.MessageBus().RegisterEventTypesWithResponse(context.Background(), common.EventTypes)
		if err != nil {
			logger.Error("error when trying to register one or more event types - some event type will not be discoverable", zap.Error(err))
		}
		if response != nil && response.StatusCode() != http.StatusOK {
			logger.Error("error when trying to register one or more event types - some event type will not be discoverable", zap.String("status", response.Status()), zap.String("body", string(response.Body)))
		}
		if response.StatusCode() == http.StatusOK {
			break
		}
		time.Sleep(time.Second)
	}

	go func() {
		time.Sleep(time.Second * 2)
		// v0.3.6
		if config.ServerInfo.HttpPort != "" {
			changePort := model.ChangePortRequest{}
			changePort.Port = config.ServerInfo.HttpPort
			err := service.MyService.Gateway().ChangePort(&changePort)
			if err == nil {
				config.Cfg.Section("server").Key("HttpPort").SetValue("")
				config.Cfg.SaveTo(config.SystemConfigInfo.ConfigPath)
			}
		}
	}()

	urlFilePath := filepath.Join(config.CommonInfo.RuntimePath, "nimoos.url")
	if err := file.CreateFileAndWriteContent(urlFilePath, "http://"+listener.Addr().String()); err != nil {
		logger.Error("error when creating address file", zap.Error(err),
			zap.Any("address", listener.Addr().String()),
			zap.Any("filepath", urlFilePath),
		)
	}

	// run any script that needs to be executed
	scriptDirectory := filepath.Join(constants.DefaultConfigPath, "start.d")
	command.ExecuteScripts(scriptDirectory)

	if supported, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		logger.Error("Failed to notify systemd that nimoos main service is ready", zap.Any("error", err))
	} else if supported {
		logger.Info("Notified systemd that nimoos main service is ready")
	} else {
		logger.Info("This process is not running as a systemd service.")
	}
	// http.HandleFunc("/v1/file/test", func(w http.ResponseWriter, r *http.Request) {

	// 	//http.ServeFile(w, r, r.URL.Path[1:])
	// 	http.ServeFile(w, r, "/DATA/test.img")
	// })
	// go http.ListenAndServe(":8081", nil)

	s := &http.Server{
		Handler: mux,
		// Increased from 5s to avoid ECONNRESET on large file uploads.
		// ReadHeaderTimeout only covers reading the request headers (not body),
		// but some Go versions/proxy chains can miscount timing under load.
		ReadHeaderTimeout: 30 * time.Second,
	}

	logger.Info("NimoOS main service is listening...", zap.Any("address", listener.Addr().String()))

	// Handle SIGTERM/SIGINT gracefully: clean up any in-progress migration before exit.
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
		<-quit
		logger.Info("shutdown signal received, cleaning up in-progress migrations")
		service.CleanupActiveMigration()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(shutdownCtx)
	}()

	// defer service.MyService.Storage().UnmountAllStorage()
	err = s.Serve(listener) // not using http.serve() to fix G114: Use of net/http serve function that has no support for setting timeouts (see https://github.com/securego/gosec)
	if err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}
