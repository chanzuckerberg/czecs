package tasks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"text/template"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/pkg/errors"
)

// ReadFileOrURI reads a file either from local disk or from the given URI.
// Auto detect whether the given string is a URI. Supported URI schemes are s3, http, or https.
func ReadFileOrURI(fileOrURI string) ([]byte, error) {
	url, err := url.ParseRequestURI(fileOrURI)
	if err != nil {
		return ioutil.ReadFile(fileOrURI)
	}
	switch url.Scheme {
	case "s3":
		sess, err := session.NewSession(&aws.Config{})
		if err != nil {
			return nil, errors.Wrap(err, "Could not create session")
		}
		svc := s3.New(sess)
		result, err := svc.GetObject(&s3.GetObjectInput{
			Bucket: &url.Host, Key: &url.Path})
		if err != nil {
			return nil, errors.Wrapf(err, "Could not retrieve S3 object %v", fileOrURI)
		}
		defer result.Body.Close()
		return ioutil.ReadAll(result.Body)
	case "http", "https":
		resp, err := http.Get(fileOrURI)
		if err != nil {
			return nil, errors.Wrapf(err, "Could not retrieve %v", fileOrURI)
		}
		defer resp.Body.Close()
		return ioutil.ReadAll(resp.Body)
	}
	return nil, fmt.Errorf("Unexpected url scheme %v in URI %v", url.Scheme, fileOrURI)
}

// ParseTaskDefinition parses an ECS task definition from a file, using the given values to fill in template variables.
// Optionally, in strict mode fail with error if a template variable makes a reference to a value
// that has not been provided.
func ParseTaskDefinition(defnFilename string, values map[string]interface{}, strict bool) (*ecs.RegisterTaskDefinitionInput, error) {
	rawDefn, err := ReadFileOrURI(defnFilename)
	if err != nil {
		return nil, errors.Wrapf(err, "Error reading task definition from %v", defnFilename)
	}
	templateOption := "missingkey=zero"
	if strict {
		// TODO(mbarrien): missingkey=error is not actually returning an error if a value is missing,
		// although it is ending the render early
		templateOption = "missingkey=error"
	}
	tmpl, err := template.New(defnFilename).Option(templateOption).Parse(string(rawDefn))
	if err != nil {
		return nil, errors.Wrap(err, "Error parsing task definition template")
	}
	var defn bytes.Buffer
	if tmpl.Execute(&defn, values); err != nil {
		return nil, errors.Wrap(err, "Error executing task definition template")
	}
	// missingkey=zero doesn't work completely properly on map[string]interface{}
	// https://github.com/golang/go/issues/24963
	// We handle this with the hard coded substitution of the string <no value> string
	filteredDefn := strings.Replace(defn.String(), "<no value>", "", -1)
	var taskDefn ecs.RegisterTaskDefinitionInput
	if err = json.Unmarshal([]byte(filteredDefn), &taskDefn); err != nil {
		return nil, errors.Wrap(err, "Error parsing JSON of task definition")
	}
	return &taskDefn, nil
}

// ParseBalances reads an arbitrary JSON file for use as values to use to replace template variable placeholders.
func ParseBalances(balancesFilename string) (map[string]interface{}, error) {
	rawBalances, err := ReadFileOrURI(balancesFilename)
	if err != nil {
		return nil, errors.Wrapf(err, "Error reading balances file %v", defnFilename)
	}
	var balances map[string]interface{}
	if err = json.Unmarshal(rawBalances, &balances); err != nil {
		return nil, errors.Wrap(err, "Error parsing JSON of balances file")
	}
	return balances, nil
}
