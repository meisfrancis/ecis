// Package awsx wraps the AWS SDK in the narrow set of calls ecis needs and
// converts SDK shapes into the model types the UI renders.
package awsx

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// costExplorerRegion is where the Cost Explorer endpoint lives. The service is
// global but only answers in us-east-1, so its client gets a pinned region
// regardless of which region the rest of the session is pointed at.
const costExplorerRegion = "us-east-1"

// Identity describes who the session is authenticated as.
type Identity struct {
	Account string
	ARN     string
	UserID  string
}

// User returns the trailing component of the caller ARN, which is the useful
// part for a header line.
func (i Identity) User() string {
	if i.ARN == "" {
		return ""
	}
	parts := strings.Split(i.ARN, "/")
	return parts[len(parts)-1]
}

// Client bundles every AWS service client ecis talks to for one profile/region
// pair. Switching context builds a new Client rather than mutating this one, so
// in-flight requests against the old context cannot bleed into the new one.
type Client struct {
	Profile string
	Region  string

	cfg aws.Config

	ECS  *ecs.Client
	CW   *cloudwatch.Client
	CE   *costexplorer.Client
	Logs *cloudwatchlogs.Client
	EC2  *ec2.Client
	AAS  *applicationautoscaling.Client
	STS  *sts.Client

	identity Identity
	// tdCache memoises described task definitions. Revisions are immutable, so
	// entries never need invalidating for the life of the session.
	tdCache sync.Map
}

// New builds a Client for the given profile and region. Empty values fall back
// to the ambient AWS environment (AWS_PROFILE, AWS_REGION, instance metadata).
func New(ctx context.Context, profile, region string) (*Client, error) {
	opts := []func(*awscfg.LoadOptions) error{}
	if profile != "" {
		opts = append(opts, awscfg.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, awscfg.WithRegion(region))
	}
	cfg, err := awscfg.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("no region configured: pass --region or set AWS_REGION")
	}

	c := &Client{
		Profile: profile,
		Region:  cfg.Region,
		cfg:     cfg,
		ECS:     ecs.NewFromConfig(cfg),
		CW:      cloudwatch.NewFromConfig(cfg),
		Logs:    cloudwatchlogs.NewFromConfig(cfg),
		EC2:     ec2.NewFromConfig(cfg),
		AAS:     applicationautoscaling.NewFromConfig(cfg),
		STS:     sts.NewFromConfig(cfg),
		CE: costexplorer.NewFromConfig(cfg, func(o *costexplorer.Options) {
			o.Region = costExplorerRegion
		}),
	}
	if c.Profile == "" {
		c.Profile = envProfile()
	}
	return c, nil
}

func envProfile() string {
	if p := os.Getenv("AWS_PROFILE"); p != "" {
		return p
	}
	return "default"
}

// Config exposes the underlying AWS config, for callers that need to build a
// client ecis does not already wrap.
func (c *Client) Config() aws.Config { return c.cfg }

// Identity resolves and caches the caller identity. A failure here is not fatal
// — the header simply shows an unknown account — so the error is returned for
// display rather than propagated as a hard failure.
func (c *Client) Identity(ctx context.Context) (Identity, error) {
	if c.identity.Account != "" {
		return c.identity, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := c.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Identity{}, err
	}
	c.identity = Identity{
		Account: aws.ToString(out.Account),
		ARN:     aws.ToString(out.Arn),
		UserID:  aws.ToString(out.UserId),
	}
	return c.identity, nil
}

// Profiles lists the profile names found in the shared AWS config and
// credentials files. It is a convenience for the context switcher; a profile
// missing from these files can still be used via --profile.
func Profiles() []string {
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
		}
	}

	for _, f := range sharedConfigFiles() {
		for _, section := range iniSections(f.path) {
			if f.stripPrefix {
				// ~/.aws/config uses "[profile name]", except for "[default]".
				if section == "default" {
					add(section)
					continue
				}
				if !strings.HasPrefix(section, "profile ") {
					continue
				}
				add(strings.TrimPrefix(section, "profile "))
				continue
			}
			// ~/.aws/credentials uses a bare "[name]".
			add(section)
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

type sharedFile struct {
	path        string
	stripPrefix bool
}

func sharedConfigFiles() []sharedFile {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	cfgPath := os.Getenv("AWS_CONFIG_FILE")
	if cfgPath == "" {
		cfgPath = filepath.Join(home, ".aws", "config")
	}
	credPath := os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	if credPath == "" {
		credPath = filepath.Join(home, ".aws", "credentials")
	}
	return []sharedFile{{cfgPath, true}, {credPath, false}}
}

// iniSections returns the section headers of an INI-style file. It is not a
// full INI parser — section names are all ecis needs.
func iniSections(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
			continue
		}
		out = append(out, strings.TrimSpace(line[1:len(line)-1]))
	}
	return out
}

// Regions returns the regions ECS is available in. This is a static list rather
// than an ec2:DescribeRegions call so the context switcher works without extra
// IAM permissions; a region missing here can still be reached via --region.
func Regions() []string {
	return []string{
		"af-south-1",
		"ap-east-1",
		"ap-northeast-1", "ap-northeast-2", "ap-northeast-3",
		"ap-south-1", "ap-south-2",
		"ap-southeast-1", "ap-southeast-2", "ap-southeast-3", "ap-southeast-4",
		"ca-central-1", "ca-west-1",
		"eu-central-1", "eu-central-2",
		"eu-north-1",
		"eu-south-1", "eu-south-2",
		"eu-west-1", "eu-west-2", "eu-west-3",
		"il-central-1",
		"me-central-1", "me-south-1",
		"sa-east-1",
		"us-east-1", "us-east-2",
		"us-west-1", "us-west-2",
	}
}
