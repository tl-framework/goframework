package goframework

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.uber.org/dig"
)

type GoFramework struct {
	ioc           *dig.Container
	configuration *viper.Viper
	server        *gin.Engine
	nrApplication gfAgentTelemetry
	healthCheck   []func() (string, bool)
}

type GoFrameworkOptions interface {
	run(gf *GoFramework)
}

func AddTenant() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		tokenString := ctx.GetHeader("Authorization")
		if tokenString == "" {
			ctx.Request.Header.Add("X-Tenant-Id", "00000000-0000-0000-0000-000000000000")
			return
		}

		tokenString = strings.Replace(tokenString, "Bearer ", "", 1)
		token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})
		if err != nil {
			ctx.AbortWithStatus(http.StatusUnauthorized)
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			if ctx.Request.Method == "POST" || ctx.Request.Method == "PUT" || ctx.Request.Method == "DELETE" {
				ctx.Request.Header.Add("X-Author", fmt.Sprint(claims["name"]))
				ctx.Request.Header.Add("X-Author-Id", fmt.Sprint(claims["sub"]))
			}

			ctx.Request.Header.Add("X-Tenant-Id", fmt.Sprint(claims["tenant_id"]))
		}
	}
}

func NewGoFramework(opts ...GoFrameworkOptions) *GoFramework {

	gf := &GoFramework{
		ioc:           dig.New(),
		configuration: initializeViper(),
		server:        gin.Default(),
		healthCheck:   make([]func() (string, bool), 0),
	}

	cconfig := cors.DefaultConfig()
	cconfig.AllowAllOrigins = true
	cconfig.AllowHeaders = []string{"*"}

	corsconfig := cors.New(cconfig)
	gf.server.Use(corsconfig, AddTenant())

	for _, opt := range opts {
		opt.run(gf)
	}

	gf.ioc.Provide(initializeViper)
	gf.ioc.Provide(newLog)
	gf.ioc.Provide(func() gfAgentTelemetry { return gf.nrApplication })

	gf.server.GET("/health", func(ctx *gin.Context) {

		list := make(map[string]bool)
		httpCode := http.StatusOK
		for _, item := range gf.healthCheck {
			name, status := item()
			list[name] = status
			if !status {
				httpCode = http.StatusServiceUnavailable
			}
		}
		ctx.JSON(httpCode, list)
	})

	if gf.nrApplication != nil {
		gf.server.Use(gf.nrApplication.gin())
	}
	err := gf.ioc.Provide(func() *gin.RouterGroup { return gf.server.Group("/") })
	if err != nil {
		log.Panic(err)
	}

	return gf
}

// VIPER
func initializeViper() *viper.Viper {
	v := viper.New()
	v.AddConfigPath("./configs")
	v.SetConfigType("json")
	v.SetConfigName(os.Getenv("env"))
	if err := v.ReadInConfig(); err != nil {
		log.Panic(err)
	}
	return v
}

func (gf *GoFramework) GetConfig(key string) string {
	return gf.configuration.GetString(key)
}

// DIG
func (gf *GoFramework) RegisterRepository(constructor interface{}) {
	err := gf.ioc.Provide(constructor)
	if err != nil {
		log.Panic(err)
	}
}

func (gf *GoFramework) RegisterApplication(application interface{}) {
	err := gf.ioc.Provide(application)
	if err != nil {
		log.Panic(err)
	}
}

// GIN
func (gf *GoFramework) RegisterController(controller interface{}) {
	err := gf.ioc.Invoke(controller)
	if err != nil {
		log.Panic(err)
	}
}

func (gf *GoFramework) Start() error {
	port := os.Getenv("port")
	if port == "" {
		port = "8081"
	}
	return gf.server.Run(":" + port)
}

func (gf *GoFramework) Invoke(function interface{}) {
	err := gf.ioc.Invoke(function)
	if err != nil {
		log.Panic(err)
	}
}

// mongo
func (gf *GoFramework) RegisterDbMongo(host string, user string, pass string, database string) {

	opts := options.Client().ApplyURI(host)

	if user != "" {
		opts.SetAuth(options.Credential{Username: user, Password: pass})
	}

	if gf.nrApplication != nil {
		opts = opts.SetMonitor(gf.nrApplication.mongoMonitor())
	}

	err := gf.ioc.Provide(func() *mongo.Database {
		cli, err := newMongoClient(opts)
		if err != nil {
			return nil
		}
		return cli.Database(database)
	})

	gf.healthCheck = append(gf.healthCheck, func() (string, bool) {
		serviceName := "MDB"
		cli, err := newMongoClient(opts)
		defer func() {
			if err = cli.Disconnect(context.TODO()); err != nil {
				panic(err)
			}
		}()

		if err != nil {
			return serviceName, false
		}

		if err := cli.Ping(context.Background(), readpref.Nearest()); err != nil {
			return serviceName, false
		}
		return serviceName, true
	})

	if err != nil {
		log.Panic(err)
	}
}

// Redis
func (gf *GoFramework) RegisterRedis(address string, password string, db string) {

	dbInt, err := strconv.Atoi(db)
	if err != nil {
		log.Panic(err)
	}

	opts := &redis.Options{
		Addr:     address,
		Password: password,
		DB:       dbInt,
	}

	if opts.Addr != "" && opts.Addr != "localhost:6379" {
		opts.TLSConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	gf.healthCheck = append(gf.healthCheck, func() (string, bool) {
		serviceName := "RDS"
		cli := newRedisClient(opts)
		if cli == nil {
			return serviceName, false
		}

		if _, err := cli.Ping(context.Background()).Result(); err != nil {
			return serviceName, false
		}
		return serviceName, true
	})

	err = gf.ioc.Provide(func() *redis.Client { return (newRedisClient(opts)) })
	if err != nil {
		log.Panic(err)
	}
}

func (gf *GoFramework) RegisterCache(constructor interface{}) {
	err := gf.ioc.Provide(constructor)
	if err != nil {
		log.Panic(err)
	}
}

func (gf *GoFramework) RegisterKafka(server string, groupId string) {
	err := gf.ioc.Provide(func() *GoKafka {
		kc := NewKafkaConfigMap(server, groupId)
		if gf.nrApplication != nil {
			kc.newMonitor(gf.nrApplication.getAgent())
		}
		return kc
	})
	if err != nil {
		log.Panic(err)
	}
}

// Kafka
func (gf *GoFramework) RegisterKafkaProducer(producer interface{}) {
	err := gf.ioc.Provide(producer)
	if err != nil {
		log.Panic(err)
	}
}

func (gf *GoFramework) RegisterKafkaConsumer(consumer interface{}) {
	err := gf.ioc.Invoke(consumer)
	if err != nil {
		log.Panic(err)
	}
}
