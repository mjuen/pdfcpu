/*
	Copyright 2020 The pdfcpu Authors.

	Licensed under the Apache License, Version 2.0 (the "License");
	you may not use this file except in compliance with the License.
	You may obtain a copy of the License at

		http://www.apache.org/licenses/LICENSE-2.0

	Unless required by applicable law or agreed to in writing, software
	distributed under the License is distributed on an "AS IS" BASIS,
	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
	See the License for the specific language governing permissions and
	limitations under the License.
*/

package api

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mjuen/pdfcpu/pkg/log"
	"github.com/mjuen/pdfcpu/pkg/pdfcpu"
	"github.com/mjuen/pdfcpu/pkg/pdfcpu/model"
	"github.com/pkg/errors"
)

// appendTo appends inFile to ctxDest's page tree.
func appendTo(rs io.ReadSeeker, fName string, ctxDest *model.Context) error {
	ctxSource, _, _, err := readAndValidate(rs, ctxDest.Configuration, time.Now())
	if err != nil {
		return err
	}

	// Merge source context into dest context.
	return pdfcpu.MergeXRefTables(fName, ctxSource, ctxDest)
}

// MergeRaw merges a sequence of PDF streams and writes the result to w.
func MergeRaw(rsc []io.ReadSeeker, w io.Writer, conf *model.Configuration) error {
	if rsc == nil {
		return errors.New("pdfcpu: MergeRaw: missing rsc")
	}

	if w == nil {
		return errors.New("pdfcpu: MergeRaw: missing w")
	}

	if conf == nil {
		conf = model.NewDefaultConfiguration()
	}
	conf.Cmd = model.MERGECREATE
	conf.ValidationMode = model.ValidationRelaxed
	conf.CreateBookmarks = false

	ctxDest, _, _, err := readAndValidate(rsc[0], conf, time.Now())
	if err != nil {
		return err
	}

	ctxDest.EnsureVersionForWriting()

	for i, f := range rsc[1:] {
		if err = appendTo(f, strconv.Itoa(i), ctxDest); err != nil {
			return err
		}
	}

	if err = OptimizeContext(ctxDest); err != nil {
		return err
	}

	return WriteContext(ctxDest, w)
}

func prepDestContext(destFile string, rs io.ReadSeeker, conf *model.Configuration) (*model.Context, error) {
	ctxDest, _, _, err := readAndValidate(rs, conf, time.Now())
	if err != nil {
		return nil, err
	}

	if conf.CreateBookmarks {
		if err := pdfcpu.EnsureOutlines(ctxDest, filepath.Base(destFile), conf.Cmd == model.MERGEAPPEND); err != nil {
			return nil, err
		}
	}

	ctxDest.EnsureVersionForWriting()

	return ctxDest, nil
}

func Merge(destFile string, inFiles []string, w io.Writer, conf *model.Configuration) error {
	if w == nil {
		return errors.New("pdfcpu: Merge: Please provide w")
	}

	if conf == nil {
		conf = model.NewDefaultConfiguration()
	}
	conf.Cmd = model.MERGECREATE
	conf.ValidationMode = model.ValidationRelaxed

	if destFile != "" {
		conf.Cmd = model.MERGEAPPEND
	}
	if destFile == "" {
		destFile = inFiles[0]
		inFiles = inFiles[1:]
	}

	f, err := os.Open(destFile)
	if err != nil {
		return err
	}
	defer f.Close()

	if conf.Cmd == model.MERGECREATE {
		if log.CLIEnabled() {
			log.CLI.Println(destFile)
		}
	}

	ctxDest, err := prepDestContext(destFile, f, conf)
	if err != nil {
		return err
	}

	for _, fName := range inFiles {
		if err := func() error {
			f, err := os.Open(fName)
			if err != nil {
				return err
			}
			defer f.Close()

			if log.CLIEnabled() {
				log.CLI.Println(fName)
			}
			if err = appendTo(f, filepath.Base(fName), ctxDest); err != nil {
				return err
			}

			return nil

		}(); err != nil {
			return err
		}
	}

	if err := OptimizeContext(ctxDest); err != nil {
		return err
	}

	return WriteContext(ctxDest, w)
}

func MergeCreateFile(inFiles []string, outFile string, conf *model.Configuration) (err error) {
	f, err := os.Create(outFile)
	if err != nil {
		return err
	}

	defer func() {
		cerr := f.Close()
		if err == nil {
			err = cerr
		}
	}()

	logWritingTo(outFile)
	return Merge("", inFiles, f, conf)
}

func MergeAppendFile(inFiles []string, outFile string, conf *model.Configuration) (err error) {
	tmpFile := outFile
	overWrite := false
	destFile := ""

	if fileExists(outFile) {
		overWrite = true
		destFile = outFile
		tmpFile += ".tmp"
		if log.CLIEnabled() {
			log.CLI.Printf("appending to %s...\n", outFile)
		}
	} else {
		logWritingTo(outFile)
	}

	f, err := os.Create(tmpFile)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			if err1 := f.Close(); err1 != nil {
				return
			}
			if overWrite {
				os.Remove(tmpFile)
			}
			return
		}
		if err = f.Close(); err != nil {
			return
		}
		if overWrite {
			err = os.Rename(tmpFile, outFile)
		}
	}()

	err = Merge(destFile, inFiles, f, conf)
	return err
}
