// Copyright 2017 Tomas Machalek <tomas.machalek@gmail.com>
// Copyright 2017 Charles University, Faculty of Arts,
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

package proc

import (
	"database/sql"
	"fmt"
	"log"

	"github.com/czcorpus/vert-tagextract/db"
	"github.com/czcorpus/vert-tagextract/db/colgen"
	_ "github.com/mattn/go-sqlite3" // sqlite3 driver load
	"github.com/tomachalek/vertigo"
)

// TTEConfProvider defines an object able to
// provide configuration data for TTExtractor factory.
type TTEConfProvider interface {
	GetCorpus() string
	GetAtomStructure() string
	GetStackStructEval() bool
	GetStructures() map[string][]string
	GetCountColumns() []int
}

// TTExtractor handles writing parsed data
// to a sqlite3 database. Parsed values are
// received pasivelly by implementing vertigo.LineProcessor
type TTExtractor struct {
	lineCounter        int
	atomCounter        int
	tokenInAtomCounter int
	corpusID           string
	database           *sql.DB
	transaction        *sql.Tx
	docInsert          *sql.Stmt
	attrAccum          attrAccumulator
	atomStruct         string
	structures         map[string][]string
	attrNames          []string
	colgenFn           colgen.AlignedColGenFn
	currAtomAttrs      map[string]interface{}
	countColumns       []int
	colCounts          map[string]*ColumnCounter
}

// NewTTExtractor is a factory function to
// instantiate proper TTExtractor.
func NewTTExtractor(database *sql.DB, conf TTEConfProvider,
	colgenFn colgen.AlignedColGenFn) *TTExtractor {
	ans := &TTExtractor{
		database:     database,
		corpusID:     conf.GetCorpus(),
		atomStruct:   conf.GetAtomStructure(),
		structures:   conf.GetStructures(),
		colgenFn:     colgenFn,
		countColumns: conf.GetCountColumns(),
		colCounts:    make(map[string]*ColumnCounter),
	}
	if conf.GetStackStructEval() {
		ans.attrAccum = newStructStack()

	} else {
		ans.attrAccum = newDefaultAccum()
	}
	return ans
}

// ProcToken is a part of vertigo.LineProcessor implementation.
// It is called by Vertigo parser when a token line is encountered.
func (tte *TTExtractor) ProcToken(tk *vertigo.Token) {
	tte.lineCounter++
	tte.tokenInAtomCounter++
	key := mkTupleKey(tk, tte.countColumns)
	cnt, ok := tte.colCounts[key]
	if !ok {
		cnt = newColumnCounter(tk, tte.countColumns)
		tte.colCounts[key] = cnt

	} else {
		cnt.IncCount()
	}
}

// ProcStructClose is a part of vertigo.LineProcessor implementation.
// It is called by Vertigo parser when a closing structure tag is
// encountered.
func (tte *TTExtractor) ProcStructClose(st *vertigo.StructureClose) {
	tte.attrAccum.end(st.Name)
	tte.lineCounter++

	if st.Name == tte.atomStruct {
		tte.currAtomAttrs["poscount"] = tte.tokenInAtomCounter

		values := make([]interface{}, len(tte.attrNames))
		for i, n := range tte.attrNames {
			if tte.currAtomAttrs[n] != nil {
				values[i] = tte.currAtomAttrs[n]

			} else {
				values[i] = "" // liveattrs plug-in does not like NULLs
			}
		}
		_, err := tte.docInsert.Exec(values...)
		if err != nil {
			log.Fatalf("Failed to insert data: %s", err)
		}
		tte.currAtomAttrs = make(map[string]interface{})
	}
}

// acceptAttr tests whether a structural attribute
// [structName].[attrName] is configured (see _example/*.json) to be imported
func (tte *TTExtractor) acceptAttr(structName string, attrName string) bool {
	tmp := tte.structures[structName]
	for _, v := range tmp {
		if v == attrName {
			return true
		}
	}
	return false
}

// ProcStruct is a part of vertigo.LineProcessor implementation.
// It si called by Vertigo parser when an opening structure tag
// is encountered.
func (tte *TTExtractor) ProcStruct(st *vertigo.Structure) {
	tte.attrAccum.begin(st)
	if st.Name == tte.atomStruct {
		tte.tokenInAtomCounter = 0
		attrs := make(map[string]interface{})
		tte.attrAccum.forEachAttr(func(s string, k string, v string) {
			if tte.acceptAttr(s, k) {
				attrs[fmt.Sprintf("%s_%s", s, k)] = v
			}
		})
		attrs["wordcount"] = 0 // This value is currently unused
		attrs["poscount"] = 0  // This value is updated once we hit the closing tag
		attrs["corpus_id"] = tte.corpusID
		if tte.colgenFn != nil {
			attrs["item_id"] = tte.colgenFn(attrs)
		}
		tte.currAtomAttrs = attrs
		tte.atomCounter++
	}
	tte.lineCounter++
}

func (tte *TTExtractor) calcNumAttrs() int {
	ans := 0
	for _, items := range tte.structures {
		ans += len(items)
	}
	return ans
}

func (tte *TTExtractor) generateAttrList() []string {
	attrNames := make([]string, tte.calcNumAttrs()+4)
	i := 0
	for s, items := range tte.structures {
		for _, item := range items {
			attrNames[i] = fmt.Sprintf("%s_%s", s, item)
			i++
		}
	}
	attrNames[i] = "wordcount"
	attrNames[i+1] = "poscount"
	attrNames[i+2] = "corpus_id"
	if tte.colgenFn != nil {
		attrNames[i+3] = "item_id"

	} else {
		attrNames = attrNames[:i+3]
	}
	return attrNames
}

func (tte *TTExtractor) insertCounts() {
	colItems := append(db.GenerateColCountNames(tte.countColumns), "corpus_id", "count")
	ins := db.PrepareInsert(tte.transaction, "colcounts", colItems)
	for _, count := range tte.colCounts {
		args := make([]interface{}, len(count.values)+2)
		for i, c := range count.values {
			args[i] = c
		}
		args[len(count.values)] = tte.corpusID
		args[len(count.values)+1] = count.count
		ins.Exec(args...)
	}
}

// Run starts the parsing and metadata extraction
// process. The method expects a proper database
// schema to be ready (see database.go for details).
// The whole process runs within a transaction which
// makes sqlite3 inserts a few orders of magnitude
// faster.
func (tte *TTExtractor) Run(conf *vertigo.ParserConf) {
	log.Print("Starting to process the vertical file...")
	tte.database.Exec("PRAGMA synchronous = OFF")
	tte.database.Exec("PRAGMA journal_mode = MEMORY")
	var err error
	tte.transaction, err = tte.database.Begin()
	if err != nil {
		log.Fatalf("Failed to start a database transaction: %s", err)
	}

	tte.attrNames = tte.generateAttrList()
	tte.docInsert = db.PrepareInsert(tte.transaction, "item", tte.attrNames)

	parserErr := vertigo.ParseVerticalFile(conf, tte)
	if parserErr != nil {
		tte.transaction.Rollback()
		log.Fatalf("Failed to parse vertical file: %s", parserErr)

	} else {
		log.Print("...DONE")
		if len(tte.countColumns) > 0 {
			log.Print("Saving defined positional attributes counts into the database...")
			tte.insertCounts()
			log.Print("...DONE")
		}
		err = tte.transaction.Commit()
		if err != nil {
			log.Fatal("Failed to commit database transaction: ", err)
		}
	}
}
