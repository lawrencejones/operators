/*
Copyright The Kubernetes Authors.

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

// Code generated by lister-gen. DO NOT EDIT.

package v1alpha1

import (
	v1alpha1 "github.com/gocardless/theatre/pkg/apis/rbac/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
)

// DirectoryRoleBindingLister helps list DirectoryRoleBindings.
type DirectoryRoleBindingLister interface {
	// List lists all DirectoryRoleBindings in the indexer.
	List(selector labels.Selector) (ret []*v1alpha1.DirectoryRoleBinding, err error)
	// DirectoryRoleBindings returns an object that can list and get DirectoryRoleBindings.
	DirectoryRoleBindings(namespace string) DirectoryRoleBindingNamespaceLister
	DirectoryRoleBindingListerExpansion
}

// directoryRoleBindingLister implements the DirectoryRoleBindingLister interface.
type directoryRoleBindingLister struct {
	indexer cache.Indexer
}

// NewDirectoryRoleBindingLister returns a new DirectoryRoleBindingLister.
func NewDirectoryRoleBindingLister(indexer cache.Indexer) DirectoryRoleBindingLister {
	return &directoryRoleBindingLister{indexer: indexer}
}

// List lists all DirectoryRoleBindings in the indexer.
func (s *directoryRoleBindingLister) List(selector labels.Selector) (ret []*v1alpha1.DirectoryRoleBinding, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha1.DirectoryRoleBinding))
	})
	return ret, err
}

// DirectoryRoleBindings returns an object that can list and get DirectoryRoleBindings.
func (s *directoryRoleBindingLister) DirectoryRoleBindings(namespace string) DirectoryRoleBindingNamespaceLister {
	return directoryRoleBindingNamespaceLister{indexer: s.indexer, namespace: namespace}
}

// DirectoryRoleBindingNamespaceLister helps list and get DirectoryRoleBindings.
type DirectoryRoleBindingNamespaceLister interface {
	// List lists all DirectoryRoleBindings in the indexer for a given namespace.
	List(selector labels.Selector) (ret []*v1alpha1.DirectoryRoleBinding, err error)
	// Get retrieves the DirectoryRoleBinding from the indexer for a given namespace and name.
	Get(name string) (*v1alpha1.DirectoryRoleBinding, error)
	DirectoryRoleBindingNamespaceListerExpansion
}

// directoryRoleBindingNamespaceLister implements the DirectoryRoleBindingNamespaceLister
// interface.
type directoryRoleBindingNamespaceLister struct {
	indexer   cache.Indexer
	namespace string
}

// List lists all DirectoryRoleBindings in the indexer for a given namespace.
func (s directoryRoleBindingNamespaceLister) List(selector labels.Selector) (ret []*v1alpha1.DirectoryRoleBinding, err error) {
	err = cache.ListAllByNamespace(s.indexer, s.namespace, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha1.DirectoryRoleBinding))
	})
	return ret, err
}

// Get retrieves the DirectoryRoleBinding from the indexer for a given namespace and name.
func (s directoryRoleBindingNamespaceLister) Get(name string) (*v1alpha1.DirectoryRoleBinding, error) {
	obj, exists, err := s.indexer.GetByKey(s.namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1alpha1.Resource("directoryrolebinding"), name)
	}
	return obj.(*v1alpha1.DirectoryRoleBinding), nil
}