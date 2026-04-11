package secret

import (
	"context"
	"fmt"
	"os"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

func Resolve(ctx context.Context, uri string) (string, error) {
	switch {
	case strings.HasPrefix(uri, "literal://"):
		return strings.TrimPrefix(uri, "literal://"), nil

	case strings.HasPrefix(uri, "env://"):
		name := strings.TrimPrefix(uri, "env://")
		val, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("environment variable %q not set", name)
		}
		return val, nil

	case strings.HasPrefix(uri, "file://"):
		path := strings.TrimPrefix(uri, "file://")
		if strings.HasPrefix(path, "~") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("expand home dir: %w", err)
			}
			path = home + path[1:]
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read secret file %q: %w", path, err)
		}
		return strings.TrimSpace(string(data)), nil

	case strings.HasPrefix(uri, "gcp://"):
		return resolveGCP(ctx, uri)

	default:
		// Check if it looks like a URI scheme we don't support
		if idx := strings.Index(uri, "://"); idx > 0 && idx < 10 {
			return "", fmt.Errorf("unsupported secret scheme %q: supported schemes are file://, gcp://, env://, literal://", uri[:idx+3])
		}
		// No scheme — treat as literal value for backwards compat
		return uri, nil
	}
}

func resolveGCP(ctx context.Context, uri string) (string, error) {
	path := strings.TrimPrefix(uri, "gcp://")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid GCP secret URI %q: expected gcp://project/secret", uri)
	}
	project := parts[0]
	secretAndVersion := parts[1]

	secretName := secretAndVersion
	version := "latest"
	if idx := strings.LastIndex(secretAndVersion, ":"); idx != -1 {
		secretName = secretAndVersion[:idx]
		version = secretAndVersion[idx+1:]
	}

	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("create secret manager client: %w", err)
	}
	defer client.Close()

	name := fmt.Sprintf("projects/%s/secrets/%s/versions/%s", project, secretName, version)
	result, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: name,
	})
	if err != nil {
		return "", fmt.Errorf("access secret %s: %w", name, err)
	}

	return string(result.Payload.Data), nil
}
