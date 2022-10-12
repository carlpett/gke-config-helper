package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"text/template"

	"cloud.google.com/go/container/apiv1"
	"cloud.google.com/go/resourcemanager/apiv3"
	"github.com/Masterminds/sprig"
	"google.golang.org/api/iterator"
	resourcemanagerpb "google.golang.org/genproto/googleapis/cloud/resourcemanager/v3"
	containerpb "google.golang.org/genproto/googleapis/container/v1"
	"gopkg.in/alecthomas/kingpin.v2"
	"gopkg.in/yaml.v3"
)

func main() {
	searchRoot := kingpin.Flag("search-root", "Folder under which to list projects").String()
	contextNameTemplate := kingpin.Flag("context-name-template", "Template to construct context name").
		Default("{{ .ProjectId }}-{{ .Name }}").String()
	kingpin.Parse()
	tpl, err := template.New("").Funcs(sprig.TxtFuncMap()).Parse(*contextNameTemplate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build context name template: %+v", err)
		os.Exit(1)
	}

	ctx := context.Background()
	projects, err := getDescendantProjects(ctx, *searchRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list projects: %+v", err)
		os.Exit(1)
	}

	clusters, err := getGKEClusters(ctx, projects)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list clusters: %+v", err)
		os.Exit(1)
	}

	cfg := kubeConfig{
		Clusters: make([]clusterConfigData, 0, len(clusters)),
		Contexts: make([]contextConfigData, 0, len(clusters)),
		Users: []userConfigData{
			{
				Name: "google-auth",
				User: userAuth{
					AuthProvider: authProvider{
						Name: "gcp",
						Config: map[string]string{
							"cmd-path":   "gke-gcloud-auth-plugin",
							"expiry-key": "{.credential.token_expiry}",
							"token-key":  "{.credential.access_token}",
						},
					},
				},
			},
		},
	}
	for _, c := range clusters {
		clusterName := fmt.Sprintf("%s/%s/%s", c.ProjectId, c.Location, c.Name)
		cfg.Clusters = append(cfg.Clusters, clusterConfigData{
			Name: clusterName,
			Cluster: clusterEndpoint{
				CAData:   c.CAData,
				Endpoint: fmt.Sprintf("https://%s", c.Endpoint),
			},
		})
		var buf bytes.Buffer
		err = tpl.Execute(&buf, c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed execute template: %+v", err)
			os.Exit(1)
		}
		cfg.Contexts = append(cfg.Contexts, contextConfigData{
			Name: buf.String(),
			Context: contextAssociation{
				Cluster: clusterName,
				User:    cfg.Users[0].Name,
			},
		})
	}

	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	enc.Encode(cfg)
}

type kubeConfig struct {
	Clusters []clusterConfigData `yaml:"clusters"`
	Contexts []contextConfigData `yaml:"contexts"`
	Users    []userConfigData    `yaml:"users"`
}
type clusterConfigData struct {
	Name    string          `yaml:"name"`
	Cluster clusterEndpoint `yaml:"cluster"`
}
type clusterEndpoint struct {
	CAData   string `yaml:"certificate-authority-data"`
	Endpoint string `yaml:"server"`
}
type contextConfigData struct {
	Name    string             `yaml:"name"`
	Context contextAssociation `yaml:"context"`
}
type contextAssociation struct {
	Cluster string `yaml:"cluster"`
	User    string `yaml:"user"`
}
type userConfigData struct {
	Name string   `yaml:"name"`
	User userAuth `yaml:"user"`
}
type userAuth struct {
	AuthProvider authProvider `yaml:"auth-provider"`
}
type authProvider struct {
	Name   string            `yaml:"name"`
	Config map[string]string `yaml:"config"`
}

func getDescendantProjects(ctx context.Context, folder string) ([]string, error) {
	fc, err := resourcemanager.NewFoldersClient(ctx)
	if err != nil {
		return nil, err
	}
	defer fc.Close()

	fi := fc.SearchFolders(ctx, &resourcemanagerpb.SearchFoldersRequest{})
	folders := make(map[string]struct {
		DisplayName string
		Parent      string
	})
	var root string
	for {
		f, err := fi.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		folders[f.Name] = struct {
			DisplayName string
			Parent      string
		}{
			DisplayName: f.DisplayName,
			Parent:      f.Parent,
		}
		if f.DisplayName == folder {
			root = f.Name
		}
	}

	// Only prune the folder tree if a search root was given
	if folder != "" {
		prune := make([]string, 0)
		for f := range folders {
			if f == root {
				continue
			}
			cur := f
			for {
				if cur == root {
					// Found root
					break
				} else if cur == folders[cur].Parent {
					// Reached top of tree without finding root
					prune = append(prune, f)
					break
				}
				cur = folders[cur].Parent
			}
		}
		for _, f := range prune {
			delete(folders, f)
		}
	}

	pc, err := resourcemanager.NewProjectsClient(ctx)
	if err != nil {
		return nil, err
	}
	defer pc.Close()

	pi := pc.SearchProjects(ctx, &resourcemanagerpb.SearchProjectsRequest{})
	projectIds := make([]string, 0)
	for {
		p, err := pi.Next()
		if err == iterator.Done {
			break
		} else if err != nil {
			return nil, err
		}

		if p.State == resourcemanagerpb.Project_DELETE_REQUESTED {
			// Skip projects pending deletion
			continue
		}
		if _, ok := folders[p.Parent]; !ok {
			// Project is not a (grand-)child of search root
			continue
		}
		// TODO: We should probably always remove children of the system-gsuite folder?
		projectIds = append(projectIds, p.Name)
	}
	return projectIds, nil
}

type clusterInfo struct {
	ProjectId string
	Location  string
	Name      string
	CAData    string
	Endpoint  string
}

func getGKEClusters(ctx context.Context, projects []string) ([]clusterInfo, error) {
	c, err := container.NewClusterManagerClient(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	data := make([]clusterInfo, 0)
	for _, p := range projects {
		r, err := c.ListClusters(ctx, &containerpb.ListClustersRequest{
			Parent: fmt.Sprintf("%s/locations/-", p),
		})
		if err != nil {
			return nil, err
		}
		for _, c := range r.Clusters {
			data = append(data, clusterInfo{
				ProjectId: strings.Split(c.SelfLink, "/")[5],
				Location:  c.Location,
				Name:      c.Name,
				CAData:    c.MasterAuth.ClusterCaCertificate,
				Endpoint:  c.Endpoint,
			})
		}
	}

	return data, nil
}
