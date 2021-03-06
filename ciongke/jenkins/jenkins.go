/*
Copyright 2016 The Kubernetes Authors.

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

package jenkins

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
)

// Status is a build result from Jenkins. If it is still building then
// Success and URL are meaningless.
type Status struct {
	Building bool
	Success  bool
	URL      string
}

type Client struct {
	client  *http.Client
	baseURL string
	user    string
	token   string
	dry     bool
}

type Build struct {
	jobName  string
	pr       int
	id       string
	queueURL *url.URL
}

const guberBase = "https://k8s-gubernator.appspot.com/build/kubernetes-jenkins/pr-logs/pull"

func NewClient(url, user, token string) *Client {
	return &Client{
		baseURL: url,
		user:    user,
		token:   token,
		client:  &http.Client{},
		dry:     false,
	}
}

func NewDryRunClient(url, user, token string) *Client {
	return &Client{
		baseURL: url,
		user:    user,
		token:   token,
		client:  &http.Client{},
		dry:     true,
	}
}

// Build triggers the job on Jenkins with an ID parameter that will let us
// track it.
func (c *Client) Build(job string, pr int, branch string) (*Build, error) {
	if c.dry {
		return &Build{}, nil
	}
	rn := rand.Int()
	buildID := fmt.Sprintf("%s-%d-%d", job, pr, rn)
	u := fmt.Sprintf("%s/job/%s/buildWithParameters?ghprbPullId=%d&ghprbTargetBranch=%s&buildId=%s", c.baseURL, job, pr, branch, buildID)
	req, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return nil, fmt.Errorf("Response not 201: %s", resp.Status)
	}
	loc, err := resp.Location()
	if err != nil {
		return nil, err
	}
	return &Build{
		jobName:  job,
		pr:       pr,
		id:       buildID,
		queueURL: loc,
	}, nil
}

// Enqueued returns whether or not the given build is in Jenkins' build queue.
func (c *Client) Enqueued(b *Build) (bool, error) {
	if c.dry {
		return false, nil
	}
	u := fmt.Sprintf("%s/queue/api/json", c.baseURL)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.SetBasicAuth(c.user, c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("Response not 2XX: %s", resp.Status)
	}
	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	queue := struct {
		Items []struct {
			Actions []struct {
				Parameters []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"parameters"`
			} `json:"actions"`
		} `json:"items"`
	}{}
	err = json.Unmarshal(buf, &queue)
	if err != nil {
		return false, err
	}
	for _, item := range queue.Items {
		for _, action := range item.Actions {
			for _, p := range action.Parameters {
				if p.Name == "buildId" && p.Value == b.id {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// Status returns the current status of the build.
func (c *Client) Status(b *Build) (*Status, error) {
	if c.dry {
		return &Status{
			Building: false,
			Success:  true,
		}, nil
	}
	u := fmt.Sprintf("%s/job/%s/api/json?tree=builds[number,result,actions[parameters[name,value]]]", c.baseURL, b.jobName)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Response not 2XX: %s", resp.Status)
	}
	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	builds := struct {
		Builds []struct {
			Actions []struct {
				Parameters []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"parameters"`
			} `json:"actions"`
			Number int     `json:"number"`
			Result *string `json:"result"`
		} `json:"builds"`
	}{}
	err = json.Unmarshal(buf, &builds)
	if err != nil {
		return nil, err
	}
	for _, build := range builds.Builds {
		for _, action := range build.Actions {
			for _, p := range action.Parameters {
				if p.Name == "buildId" && p.Value == b.id {
					if build.Result == nil {
						return &Status{Building: true}, nil
					} else {
						return &Status{
							Building: false,
							Success:  *build.Result == "SUCCESS",
							URL:      fmt.Sprintf("%s/%d/%s/%d/", guberBase, b.pr, b.jobName, build.Number),
						}, nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("did not find build %s", b.id)
}
