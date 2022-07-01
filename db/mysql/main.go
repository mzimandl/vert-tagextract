// Copyright 2022 Tomas Machalek <tomas.machalek@gmail.com>
// Copyright 2022 Charles University, Faculty of Arts,
//                Institute of the Czech National Corpus
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mysql

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/czcorpus/vert-tagextract/v2/cnf"
	"github.com/czcorpus/vert-tagextract/v2/db"

	"github.com/go-sql-driver/mysql"
)

func joinArgs(args []string) string {
	return strings.Join(args, ", ")
}

type Writer struct {
	database *sql.DB
	tx       *sql.Tx
	dbName   string

	// groupedCorpusName represents a derived corpus name which is able to group multiple
	// (aligned) corpora together (e.g. intercorp_v13_en, intercorp_v13_cs => intercorp_v13)
	groupedCorpusName string

	Structures   map[string][]string
	IndexedCols  []string
	SelfJoinConf db.SelfJoinConf
	BibViewConf  db.BibViewConf
	CountColumns []int
}

func (w *Writer) DatabaseExists() bool {
	row := w.database.QueryRow(
		`SELECT COUNT(*) > 0 FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`,
		w.dbName, w.groupedCorpusName+"_item",
	)
	var ans bool
	err := row.Scan(&ans)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		log.Print("ERROR: failed to test data storage existence - ", err)
		return false
	}
	return ans
}

func (w *Writer) Initialize(appendMode bool) error {
	var err error
	dbExisted := w.DatabaseExists()
	if !appendMode {
		if dbExisted {
			log.Printf(
				"The data storage %s/%s already exists. Existing data will be deleted.",
				w.dbName, w.groupedCorpusName,
			)
			err := dropExisting(w.database, w.groupedCorpusName)
			if err != nil {
				return err
			}
		}
		err := createSchema(
			w.database,
			w.groupedCorpusName,
			w.Structures,
			w.IndexedCols,
			w.SelfJoinConf.IsConfigured(),
			w.CountColumns,
		)
		if err != nil {
			return err
		}
		if w.BibViewConf.IsConfigured() {
			err := createBibView(
				w.database, w.groupedCorpusName, w.BibViewConf.Cols, w.BibViewConf.IDAttr)
			if err != nil {
				return err
			}
		}
	}

	w.tx, err = w.database.Begin()
	return err
}

func (w *Writer) PrepareInsert(table string, attrs []string) (db.InsertOperation, error) {
	if w.tx == nil {
		return nil, fmt.Errorf("cannot prepare insert - no transaction active")
	}
	valReplac := make([]string, len(attrs))
	for i := range attrs {
		valReplac[i] = "?"
	}
	stmt, err := w.tx.Prepare(
		fmt.Sprintf("INSERT INTO %s_%s (%s) VALUES (%s)", w.groupedCorpusName, table, joinArgs(attrs), joinArgs(valReplac)))
	if err != nil {
		return nil, fmt.Errorf("failed to prepare INSERT: %s", err)
	}
	return &db.Insert{Stmt: stmt}, nil
}

func (w *Writer) Commit() error {
	return w.tx.Commit()
}

func (w *Writer) Rollback() error {
	return w.tx.Rollback()
}

func (w *Writer) Close() {
	err := w.database.Close()
	if err != nil {
		log.Print("WARNING: error closing database - ", err)
	}
}

func NewWriter(conf *cnf.VTEConf) (*Writer, error) {

	mconf := mysql.NewConfig()
	mconf.Net = "tcp"
	mconf.Addr = conf.DB.Host
	mconf.User = conf.DB.User
	mconf.Passwd = conf.DB.Password
	mconf.DBName = conf.DB.Name
	mconf.ParseTime = true
	mconf.Loc = time.Local
	db, err := sql.Open("mysql", mconf.FormatDSN())
	if err != nil {
		return nil, err
	}
	groupedCorpusName := conf.Corpus
	if conf.ParallelCorpus != "" {
		groupedCorpusName = conf.ParallelCorpus
	}
	return &Writer{
		database:          db,
		dbName:            conf.DB.Name,
		groupedCorpusName: groupedCorpusName,
		Structures:        conf.Structures,
		IndexedCols:       conf.IndexedCols,
		SelfJoinConf:      conf.SelfJoin,
		BibViewConf:       conf.BibView,
		CountColumns:      conf.Ngrams.AttrColumns,
	}, nil
}
