package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/Nerzal/gocloak/v13"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type Config struct {
	NamespaceFilter string
	GroupPostfixes  []string
	KeycloakURL     string
	KeycloakUser    string
	KeycloakPass    string
	Realm           string
	GroupsPrefix    string
}

func getNamespaces(clientset *kubernetes.Clientset, filter string) ([]string, error) {
	regex, err := regexp.Compile(filter)
	if err != nil {
		return nil, fmt.Errorf("invalid filter regex: %v", err)
	}

	nsList, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %v", err)
	}

	var namespaces []string
	for _, ns := range nsList.Items {
		if regex.MatchString(ns.Name) {
			namespaces = append(namespaces, ns.Name)
		}
	}
	return namespaces, nil
}

func syncGroups(client *gocloak.GoCloak, token, realm string, namespaces, postfixes []string, GroupsPrefix string) error {
	existingGroups, err := client.GetGroups(context.TODO(), token, realm, gocloak.GetGroupsParams{})
	if err != nil {
		return fmt.Errorf("failed to fetch groups: %v", err)
	}

	existingGroupSet := make(map[string]bool)
	for _, group := range existingGroups {
		existingGroupSet[*group.Name] = true
	}

	desiredGroups := make(map[string]bool)
	for _, ns := range namespaces {
		for _, postfix := range postfixes {
			groupName := fmt.Sprintf("%s-%s", ns, postfix)
			desiredGroups[groupName] = true
			if !existingGroupSet[groupName] {
				_, err := client.CreateGroup(context.TODO(), token, realm, gocloak.Group{Name: &groupName})
				if err != nil {
					log.Printf("failed to create group %s: %v", groupName, err)
				}
			}
		}
	}

	for groupName := range existingGroupSet {
		if strings.HasPrefix(groupName, GroupsPrefix) && !desiredGroups[groupName] {
			err := client.DeleteGroup(context.TODO(), token, realm, groupName)
			if err != nil {
				log.Printf("failed to delete group %s: %v", groupName, err)
			}
		}
	}
	return nil
}

func watchNamespaces(clientset *kubernetes.Clientset, namespaceFilter string, postfixes []string, client *gocloak.GoCloak, token string, realm string) {
	watcher, err := clientset.CoreV1().Namespaces().Watch(context.TODO(), metav1.ListOptions{
		LabelSelector: namespaceFilter,
	})
	if err != nil {
		log.Fatalf("failed to start watch for namespaces: %v", err)
	}
	defer watcher.Stop()

	log.Println("Watching for namespace changes...")

	for event := range watcher.ResultChan() {
		switch event.Type {
		case watch.Added:
			ns := event.Object.(*v1.Namespace)
			log.Printf("Namespace added: %s", ns.Name)
			createGroupsForNamespace(client, token, realm, ns.Name, postfixes)

		case watch.Deleted:
			ns := event.Object.(*v1.Namespace)
			log.Printf("Namespace deleted: %s", ns.Name)
			deleteGroupsForNamespace(client, token, realm, ns.Name, postfixes)

		case watch.Modified:
			ns := event.Object.(*v1.Namespace)
			log.Printf("Namespace modified: %s", ns.Name)
		}
	}
}

func createGroupsForNamespace(client *gocloak.GoCloak, token string, realm string, namespace string, postfixes []string) {
	for _, postfix := range postfixes {
		groupName := fmt.Sprintf("%s-%s", namespace, postfix)
		groups, err := client.GetGroups(context.TODO(), token, realm, gocloak.GetGroupsParams{})
		if err != nil {
			log.Printf("failed to fetch groups: %v", err)
			return
		}

		var groupExists bool
		for _, group := range groups {
			if *group.Name == groupName {
				groupExists = true
				break
			}
		}

		if !groupExists {
			_, err := client.CreateGroup(context.TODO(), token, realm, gocloak.Group{Name: &groupName})
			if err != nil {
				log.Printf("failed to create group %s: %v", groupName, err)
			} else {
				log.Printf("Created group: %s", groupName)
			}
		}
	}
}

func deleteGroupsForNamespace(client *gocloak.GoCloak, token string, realm string, namespace string, postfixes []string) {
	for _, postfix := range postfixes {
		groupName := fmt.Sprintf("%s-%s", namespace, postfix)
		err := client.DeleteGroup(context.TODO(), token, realm, groupName)
		if err != nil {
			log.Printf("failed to delete group %s: %v", groupName, err)
		} else {
			log.Printf("Deleted group: %s", groupName)
		}
	}
}

func main() {
	config := Config{
		NamespaceFilter: os.Getenv("NAMESPACE_FILTER"),
		GroupPostfixes:  strings.Split(os.Getenv("GROUP_POSTFIXES"), ","),
		KeycloakURL:     os.Getenv("KEYCLOAK_URL"),
		KeycloakUser:    os.Getenv("KEYCLOAK_USER"),
		KeycloakPass:    os.Getenv("KEYCLOAK_PASS"),
		Realm:           os.Getenv("KEYCLOAK_REALM"),
		GroupsPrefix:    os.Getenv("GROUPS_PREFIX"),
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("failed to create incluster config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create Kubernetes client: %v", err)
	}

	client := gocloak.NewClient(config.KeycloakURL)

	token, err := client.LoginAdmin(context.TODO(), config.KeycloakUser, config.KeycloakPass, "master")
	if err != nil {
		log.Fatalf("failed to login to Keycloak: %v", err)
	}

	namespaces, err := getNamespaces(clientset, config.NamespaceFilter)
	if err != nil {
		log.Fatalf("failed to get namespaces: %v", err)
	}

	err = syncGroups(client, token.AccessToken, config.Realm, namespaces, config.GroupPostfixes, config.GroupsPrefix)
	if err != nil {
		log.Fatalf("failed to sync groups: %v", err)
	}

	watchNamespaces(clientset, config.NamespaceFilter, config.GroupPostfixes, client, token.AccessToken, config.Realm)
}
