// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/conf"
	"github.com/hanzoai/compute/util"
)

var logPostOnly bool

func init() {
	logPostOnly = conf.GetConfigBool("logPostOnly")
}

type Record struct {
	Id int `xorm:"int notnull pk autoincr" json:"id"`

	Owner       string `xorm:"varchar(100) index" json:"owner"`
	Name        string `xorm:"varchar(100) index" json:"name"`
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`

	Organization string `xorm:"varchar(100)" json:"organization"`
	ClientIp     string `xorm:"varchar(100)" json:"clientIp"`
	UserAgent    string `xorm:"varchar(100)" json:"userAgent"`
	User         string `xorm:"varchar(100)" json:"user"`
	Method       string `xorm:"varchar(100)" json:"method"`
	RequestUri   string `xorm:"varchar(1000)" json:"requestUri"`
	Action       string `xorm:"varchar(1000)" json:"action"`
	Language     string `xorm:"varchar(100)" json:"language"`

	Object   string `xorm:"mediumtext" json:"object"`
	Response string `xorm:"mediumtext" json:"response"`
	// ExtendedUser *User  `xorm:"-" json:"extendedUser"`

	Provider    string `xorm:"varchar(100)" json:"provider"`
	Block       string `xorm:"varchar(100)" json:"block"`
	Transaction string `xorm:"varchar(500)" json:"transaction"`
	IsTriggered bool   `json:"isTriggered"`
}

type Response struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
}

// RecordResponseKey is the request-context local under which a handler stashes
// its JSON response envelope (via ResponseOk/ResponseError). The record filter
// reads it back after the handler returns to build the audit record — the ONE
// key shared by the writer (controllers) and the reader (routers).
const RecordResponseKey = "record.responseJson"

func GetRecordCount(owner, field, value string) (int64, error) {
	session := GetSession(owner, -1, -1, field, value, "", "")
	return session.Count(&Record{Owner: owner})
}

func GetRecords(owner string) ([]*Record, error) {
	records := []*Record{}
	engine, err := EngineFor(owner)
	if err != nil {
		return records, err
	}
	err = engine.Desc("id").Find(&records, &Record{Owner: owner})
	if err != nil {
		return records, err
	}

	return records, nil
}

func GetPaginationRecords(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Record, error) {
	records := []*Record{}
	session := GetSession(owner, offset, limit, field, value, sortField, sortOrder)
	err := session.Find(&records)
	if err != nil {
		return records, err
	}

	return records, nil
}

func getRecord(owner string, name string) (*Record, error) {
	if owner == "" || name == "" {
		return nil, nil
	}

	engine, err := EngineFor(owner)
	if err != nil {
		return nil, err
	}
	record := Record{Name: name}
	existed, err := engine.Get(&record)
	if err != nil {
		return &record, err
	}

	if existed {
		return &record, nil
	} else {
		return nil, nil
	}
}

func GetRecord(id string) (*Record, error) {
	owner, name := util.GetOwnerAndNameFromIdNoCheck(id)
	return getRecord(owner, name)
}

func UpdateRecord(id string, record *Record) (bool, error) {
	owner, name := util.GetOwnerAndNameFromId(id)
	if p, err := getRecord(owner, name); err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}

	engine, err := EngineFor(owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.Where("name = ?", name).AllCols().Update(record)
	if err != nil {
		return false, err
	}

	return affected != 0, nil
}

// NewRecord builds an audit record from the ZAP request context and the
// response payload the handler produced (respJSON — the value ResponseOk/
// ResponseError stashed for the record filter). respJSON stands in for Beego's
// ctx.Input.Data()["json"]: the ONE thing the record needs from the response is
// its {status,msg} envelope.
func NewRecord(c *zip.Ctx, respJSON any) (*Record, error) {
	ip := strings.Replace(util.ClientIPFromCtx(c), ": ", "", -1)
	action := strings.Replace(c.Path(), "/v1/", "", -1)
	requestUri := util.FilterQuery(c.Fiber().OriginalURL(), []string{"accessToken"})
	if len(requestUri) > 1000 {
		requestUri = requestUri[0:1000]
	}

	object := ""
	if body := c.Body(); len(body) != 0 {
		object = string(body)
	}

	respBytes, err := json.Marshal(respJSON)
	if err != nil {
		return nil, err
	}

	var resp Response
	err = json.Unmarshal(respBytes, &resp)
	if err != nil {
		return nil, err
	}

	language := c.Header("Accept-Language")
	if len(language) > 2 {
		language = language[0:2]
	}
	languageCode := conf.GetLanguage(language)

	record := Record{
		Name:        uuid.NewString(),
		CreatedTime: util.GetCurrentTime(),
		ClientIp:    ip,
		User:        "",
		Method:      c.Method(),
		RequestUri:  requestUri,
		Action:      action,
		Language:    languageCode,
		Object:      object,
		Response:    fmt.Sprintf("{\"status\":\"%s\",\"msg\":\"%s\"}", resp.Status, resp.Msg),
		IsTriggered: false,
	}
	return &record, nil
}

func AddRecord(record *Record) bool {
	if logPostOnly {
		if record.Method == "GET" {
			return false
		}
	}

	if strings.HasSuffix(record.Action, "-record") {
		return false
	}

	if record.Provider == "" {
		provider, err := getActiveBlockchainProvider(record.Organization)
		if err != nil {
			panic(err)
		}

		if provider != nil {
			record.Provider = provider.Name
		}
	}

	record.Owner = record.Organization

	affected, err := mustEngineFor(record.Owner).Insert(record)
	if err != nil {
		panic(err)
	}

	return affected != 0
}

func DeleteRecord(record *Record) (bool, error) {
	engine, err := EngineFor(record.Owner)
	if err != nil {
		return false, err
	}
	affected, err := engine.Where("name = ?", record.Name).Delete(&Record{})
	if err != nil {
		return false, err
	}

	return affected != 0, nil
}

func (record *Record) getId() string {
	return fmt.Sprintf("%s/%s", record.Owner, record.Name)
}
