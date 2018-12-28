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
	v1alpha1 "github.com/lawrencejones/theatre/pkg/apis/rbac/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
)

// SudoRoleLister helps list SudoRoles.
type SudoRoleLister interface {
	// List lists all SudoRoles in the indexer.
	List(selector labels.Selector) (ret []*v1alpha1.SudoRole, err error)
	// SudoRoles returns an object that can list and get SudoRoles.
	SudoRoles(namespace string) SudoRoleNamespaceLister
	SudoRoleListerExpansion
}

// sudoRoleLister implements the SudoRoleLister interface.
type sudoRoleLister struct {
	indexer cache.Indexer
}

// NewSudoRoleLister returns a new SudoRoleLister.
func NewSudoRoleLister(indexer cache.Indexer) SudoRoleLister {
	return &sudoRoleLister{indexer: indexer}
}

// List lists all SudoRoles in the indexer.
func (s *sudoRoleLister) List(selector labels.Selector) (ret []*v1alpha1.SudoRole, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha1.SudoRole))
	})
	return ret, err
}

// SudoRoles returns an object that can list and get SudoRoles.
func (s *sudoRoleLister) SudoRoles(namespace string) SudoRoleNamespaceLister {
	return sudoRoleNamespaceLister{indexer: s.indexer, namespace: namespace}
}

// SudoRoleNamespaceLister helps list and get SudoRoles.
type SudoRoleNamespaceLister interface {
	// List lists all SudoRoles in the indexer for a given namespace.
	List(selector labels.Selector) (ret []*v1alpha1.SudoRole, err error)
	// Get retrieves the SudoRole from the indexer for a given namespace and name.
	Get(name string) (*v1alpha1.SudoRole, error)
	SudoRoleNamespaceListerExpansion
}

// sudoRoleNamespaceLister implements the SudoRoleNamespaceLister
// interface.
type sudoRoleNamespaceLister struct {
	indexer   cache.Indexer
	namespace string
}

// List lists all SudoRoles in the indexer for a given namespace.
func (s sudoRoleNamespaceLister) List(selector labels.Selector) (ret []*v1alpha1.SudoRole, err error) {
	err = cache.ListAllByNamespace(s.indexer, s.namespace, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha1.SudoRole))
	})
	return ret, err
}

// Get retrieves the SudoRole from the indexer for a given namespace and name.
func (s sudoRoleNamespaceLister) Get(name string) (*v1alpha1.SudoRole, error) {
	obj, exists, err := s.indexer.GetByKey(s.namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1alpha1.Resource("sudorole"), name)
	}
	return obj.(*v1alpha1.SudoRole), nil
}
