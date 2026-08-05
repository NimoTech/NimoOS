package httper

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/NimoTech/NimoOS/pkg/config"
	"github.com/tidwall/gjson"
)

// Send a GET request.
// url: request address
// response: content of the response
func Get(url string, head map[string]string) (response string) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", url, nil)

	for k, v := range head {
		req.Header.Add(k, v)
	}
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		// TODO: handle error logging here
		// logger.Error(error)
		return ""
		// panic(error)
	}
	defer resp.Body.Close()
	var buffer [512]byte
	result := bytes.NewBuffer(nil)
	for {
		n, err := resp.Body.Read(buffer[0:])
		result.Write(buffer[0:n])
		if err != nil && err == io.EOF {
			break
		} else if err != nil {
			// logger.Error(err)
			return ""
			//	panic(err)
		}
	}
	response = result.String()
	return
}

// Send a GET request.
// url: request address
// response: content of the response
func PersonGet(url string) (response string) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		// TODO: handle error logging here
		// logger.Error(error)
		return ""
		// panic(error)
	}
	defer resp.Body.Close()
	var buffer [512]byte
	result := bytes.NewBuffer(nil)
	for {
		n, err := resp.Body.Read(buffer[0:])
		result.Write(buffer[0:n])
		if err != nil && err == io.EOF {
			break
		} else if err != nil {
			// logger.Error(err)
			return ""
			//	panic(err)
		}
	}
	response = result.String()
	return
}

// Send a POST request.
// url: request address, data: data submitted in the POST request, contentType: request body format, e.g. application/json
// content: content of the response
func Post(url string, data []byte, contentType string, head map[string]string) (content string) {
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(data))
	req.Header.Add("content-type", contentType)
	for k, v := range head {
		req.Header.Add(k, v)
	}
	if err != nil {
		panic(err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, error := client.Do(req)
	if error != nil {
		fmt.Println(error)
		return
	}
	defer resp.Body.Close()

	result, _ := ioutil.ReadAll(resp.Body)
	content = string(result)
	return
}

// Send a POST request.
// url: request address, data: data submitted in the POST request, contentType: request body format, e.g. application/json
// content: content of the response
func ZeroTierGet(url string, head map[string]string) (content string, code int) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range head {
		req.Header.Add(k, v)
	}
	if err != nil {
		panic(err)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, error := client.Do(req)

	if error != nil {
		panic(error)
	}
	defer resp.Body.Close()
	code = resp.StatusCode
	result, _ := ioutil.ReadAll(resp.Body)
	content = string(result)
	return
}

// Send a GET request.
// url: request address
// response: content of the response
func OasisGet(url string) (response string) {
	// An unset ServerApi means no remote service is configured, so make no
	// outbound request. Without this the token fetch below would build a URL with
	// no scheme or host and rely on the HTTP client failing fast — which it does,
	// but "unconfigured means offline" should be a contract in the code rather
	// than a property of whichever client happens to be in use.
	if config.ServerInfo.ServerApi == "" {
		return ""
	}

	head := make(map[string]string)

	t := make(chan string)

	go func() {
		str := Get(config.ServerInfo.ServerApi+"/token", nil)

		t <- gjson.Get(str, "data").String()
	}()
	head["Authorization"] = <-t

	return Get(url, head)
}
