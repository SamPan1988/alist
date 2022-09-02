package teambition

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/drivers/base"
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

// do others that not defined in Driver interface

func (d *Teambition) isInternational() bool {
	return d.Region == "international"
}

func (d *Teambition) request(pathname string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	url := "https://www.teambition.com" + pathname
	if d.isInternational() {
		url = "https://us.teambition.com" + pathname
	}
	req := base.RestyClient.R()
	req.SetHeader("Cookie", d.Cookie)
	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	var e ErrResp
	req.SetError(&e)
	res, err := req.Execute(method, url)
	if err != nil {
		return nil, err
	}
	if e.Name != "" {
		return nil, errors.New(e.Message)
	}
	return res.Body(), nil
}

func (d *Teambition) getFiles(parentId string) ([]model.Obj, error) {
	files := make([]model.Obj, 0)
	page := 1
	for {
		var collections []Collection
		_, err := d.request("/api/collections", http.MethodGet, func(req *resty.Request) {
			req.SetQueryParams(map[string]string{
				"_parentId":  parentId,
				"_projectId": d.ProjectID,
				"order":      d.OrderBy + d.OrderDirection,
				"count":      "50",
				"page":       strconv.Itoa(page),
			})
		}, &collections)
		if err != nil {
			return nil, err
		}
		if len(collections) == 0 {
			break
		}
		page++
		for _, collection := range collections {
			if collection.Title == "" {
				continue
			}
			files = append(files, &model.Object{
				ID:       collection.ID,
				Name:     collection.Title,
				IsFolder: true,
				Modified: collection.Updated,
			})
		}
	}
	page = 1
	for {
		var works []Work
		_, err := d.request("/api/works", http.MethodGet, func(req *resty.Request) {
			req.SetQueryParams(map[string]string{
				"_parentId":  parentId,
				"_projectId": d.ProjectID,
				"order":      d.OrderBy + d.OrderDirection,
				"count":      "50",
				"page":       strconv.Itoa(page),
			})
		}, &works)
		if err != nil {
			return nil, err
		}
		if len(works) == 0 {
			break
		}
		page++
		for _, work := range works {
			files = append(files, &model.ObjThumbURL{
				Object: model.Object{
					ID:       work.ID,
					Name:     work.FileName,
					Size:     work.FileSize,
					Modified: work.Updated,
				},
				Thumbnail: model.Thumbnail{Thumbnail: work.Thumbnail},
				Url:       model.Url{Url: work.DownloadURL},
			})
		}
	}
	return files, nil
}

func (d *Teambition) upload(file model.FileStreamer, token string) (*FileUpload, error) {
	prefix := "tcs"
	if d.isInternational() {
		prefix = "us-tcs"
	}
	var newFile FileUpload
	_, err := base.RestyClient.R().SetResult(&newFile).SetHeader("Authorization", token).
		SetMultipartFormData(map[string]string{
			"name": file.GetName(),
			"type": file.GetMimetype(),
			"size": strconv.FormatInt(file.GetSize(), 10),
			//"lastModifiedDate": "",
		}).SetMultipartField("file", file.GetName(), file.GetMimetype(), file).
		Post(fmt.Sprintf("https://%s.teambition.net/upload", prefix))
	if err != nil {
		return nil, err
	}
	return &newFile, nil
}

func (d *Teambition) chunkUpload(file model.FileStreamer, token string, up driver.UpdateProgress) (*FileUpload, error) {
	prefix := "tcs"
	referer := "https://www.teambition.com/"
	if d.isInternational() {
		prefix = "us-tcs"
		referer = "https://us.teambition.com/"
	}
	var newChunk ChunkUpload
	_, err := base.RestyClient.R().SetResult(&newChunk).SetHeader("Authorization", token).
		SetBody(base.Json{
			"fileName":    file.GetName(),
			"fileSize":    file.GetSize(),
			"lastUpdated": time.Now(),
		}).Post(fmt.Sprintf("https://%s.teambition.net/upload/chunk", prefix))
	if err != nil {
		return nil, err
	}
	for i := 0; i < newChunk.Chunks; i++ {
		chunkSize := newChunk.ChunkSize
		if i == newChunk.Chunks-1 {
			chunkSize = int(file.GetSize()) - i*chunkSize
		}
		log.Debugf("%d : %d", i, chunkSize)
		chunkData := make([]byte, chunkSize)
		_, err = io.ReadFull(file, chunkData)
		if err != nil {
			return nil, err
		}
		u := fmt.Sprintf("https://%s.teambition.net/upload/chunk/%s?chunk=%d&chunks=%d",
			prefix, newChunk.FileKey, i+1, newChunk.Chunks)
		log.Debugf("url: %s", u)
		_, err := base.RestyClient.R().SetHeaders(map[string]string{
			"Authorization": token,
			"Content-Type":  "application/octet-stream",
			"Referer":       referer,
		}).SetBody(chunkData).Post(u)
		if err != nil {
			return nil, err
		}
		if err != nil {
			return nil, err
		}
		up(i * 100 / newChunk.Chunks)
	}
	_, err = base.RestyClient.R().SetHeader("Authorization", token).Post(
		fmt.Sprintf("https://%s.teambition.net/upload/chunk/%s",
			prefix, newChunk.FileKey))
	if err != nil {
		return nil, err
	}
	return &newChunk.FileUpload, nil
}

func (d *Teambition) finishUpload(file *FileUpload, parentId string) error {
	file.InvolveMembers = []interface{}{}
	file.Visible = "members"
	file.ParentId = parentId
	_, err := d.request("/api/works", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"works":     []FileUpload{*file},
			"_parentId": parentId,
		})
	}, nil)
	return err
}

func GetBetweenStr(str, start, end string) string {
	n := strings.Index(str, start)
	if n == -1 {
		return ""
	}
	n = n + len(start)
	str = string([]byte(str)[n:])
	m := strings.Index(str, end)
	if m == -1 {
		return ""
	}
	str = string([]byte(str)[:m])
	return str
}
