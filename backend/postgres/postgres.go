// Copyright © 2018 by PACE Telematics GmbH. All rights reserved.
// Created at 2018/09/12 by Vincent Landgraf

// Package postgres helps creating PostgreSQL connection pools
package postgres

import (
	"fmt"
	"time"

	"github.com/caarlos0/env"
	"github.com/go-pg/pg"
	opentracing "github.com/opentracing/opentracing-go"
	olog "github.com/opentracing/opentracing-go/log"
	"github.com/rs/zerolog"
	"lab.jamit.de/pace/go-microservice/maintenance/log"
)

type config struct {
	Port     int    `env:"POSTGRES_PORT" envDefault:"5432"`
	Host     string `env:"POSTGRES_HOST" envDefault:"localhost"`
	Password string `env:"POSTGRES_PASSWORD" envDefault:"pace1234!"`
	User     string `env:"POSTGRES_USER" envDefault:"postgres"`
	Database string `env:"POSTGRES_DB" envDefault:"postgres"`
}

var cfg config

func init() {
	// parse log config
	err := env.Parse(&cfg)
	if err != nil {
		log.Fatalf("Failed to parse postgres environment: %v", err)
	}
}

// ConnectionPool returns a new database connection pool
// that is already configured with the correct credentials and
// instrumented with tracing and logging
func ConnectionPool() *pg.DB {
	return CustomConnectionPool(&pg.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		User:     cfg.User,
		Password: cfg.Password,
		Database: cfg.Database,
	})
}

// CustomConnectionPool returns a new database connection pool
// that is already configured with the correct credentials and
// instrumented with tracing and logging using the passed options
func CustomConnectionPool(opts *pg.Options) *pg.DB {
	log.Logger().Info().Str("addr", opts.Addr).
		Str("user", opts.User).Str("database", opts.Database).
		Msg("PostgreSQL connection pool created")
	db := pg.Connect(opts)
	db.OnQueryProcessed(queryLogger)
	db.OnQueryProcessed(openTracingAdapter)
	return db
}

func queryLogger(event *pg.QueryProcessedEvent) {
	ctx := event.DB.Context()
	dur := float64(time.Since(event.StartTime)) / float64(time.Millisecond)

	// check if log context is given
	var logger *zerolog.Logger
	if ctx != nil {
		logger = log.Ctx(ctx)
	} else {
		logger = log.Logger()
	}

	// add general info
	le := logger.Debug().
		Str("file", event.File).
		Int("line", event.Line).
		Str("func", event.Func).
		Int("attempt", event.Attempt).
		Float64("duration", dur)

	// add error or result set info
	if event.Error != nil {
		le = le.Err(event.Error)
	} else {
		le = le.Int("affected", event.Result.RowsAffected()).
			Int("rows", event.Result.RowsReturned())
	}

	q, qe := event.FormattedQuery()
	if qe != nil {
		// this is only a display issue not a "real" issue
		le.Msgf("%v", qe)
	}
	le.Msg(q)
}

func openTracingAdapter(event *pg.QueryProcessedEvent) {
	// start span with general info
	q, qe := event.FormattedQuery()
	if qe != nil {
		// this is only a display issue not a "real" issue
		q = qe.Error()
	}

	name := fmt.Sprintf("PostgreSQL: %s", q)
	span, _ := opentracing.StartSpanFromContext(event.DB.Context(), name,
		opentracing.StartTime(event.StartTime))

	fields := []olog.Field{
		olog.String("file", event.File),
		olog.Int("line", event.Line),
		olog.String("func", event.Func),
		olog.Int("attempt", event.Attempt),
		olog.String("query", q),
	}

	// add error or result set info
	if event.Error != nil {
		fields = append(fields, olog.Error(event.Error))
	} else {
		fields = append(fields,
			olog.Int("affected", event.Result.RowsAffected()),
			olog.Int("rows", event.Result.RowsReturned()))
	}

	span.LogFields(fields...)
	span.Finish()
}