package awsx

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/meisfrancis/ecis/internal/model"
)

// AWS caps how many identifiers a single describe call accepts. Listing is
// paginated, so a full refresh fans the ARNs back out across batched describes.
const (
	describeServicesBatch = 10
	describeTasksBatch    = 100
	describeInstBatch     = 100
	// maxParallelDescribes bounds concurrent describe calls so a cluster with
	// hundreds of services does not trip ECS request throttling.
	maxParallelDescribes = 8
)

// ShortARN returns the resource name at the tail of an ARN.
func ShortARN(arn string) string {
	if arn == "" {
		return ""
	}
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// Clusters lists every cluster in the region with its statistics.
func (c *Client) Clusters(ctx context.Context) ([]model.Cluster, error) {
	var arns []string
	pager := ecs.NewListClustersPaginator(c.ECS, &ecs.ListClustersInput{})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list clusters: %w", err)
		}
		arns = append(arns, page.ClusterArns...)
	}
	if len(arns) == 0 {
		return nil, nil
	}

	var (
		mu  sync.Mutex
		out []model.Cluster
	)
	err := forEachBatch(ctx, arns, describeServicesBatch, func(ctx context.Context, batch []string) error {
		res, err := c.ECS.DescribeClusters(ctx, &ecs.DescribeClustersInput{
			Clusters: batch,
			Include: []ecstypes.ClusterField{
				ecstypes.ClusterFieldStatistics,
				ecstypes.ClusterFieldSettings,
				ecstypes.ClusterFieldTags,
			},
		})
		if err != nil {
			return fmt.Errorf("describe clusters: %w", err)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, cl := range res.Clusters {
			out = append(out, toCluster(cl))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func toCluster(c ecstypes.Cluster) model.Cluster {
	out := model.Cluster{
		Name:           aws.ToString(c.ClusterName),
		ARN:            aws.ToString(c.ClusterArn),
		Status:         aws.ToString(c.Status),
		ActiveServices: int(c.ActiveServicesCount),
		RunningTasks:   int(c.RunningTasksCount),
		PendingTasks:   int(c.PendingTasksCount),
		Instances:      int(c.RegisteredContainerInstancesCount),
		Settings:       map[string]string{},
		Statistics:     map[string]string{},
		Tags:           map[string]string{},
		Raw:            c,
	}
	for _, cp := range c.CapacityProviders {
		out.CapacityProviders = append(out.CapacityProviders, cp)
	}
	for _, s := range c.Settings {
		out.Settings[string(s.Name)] = aws.ToString(s.Value)
	}
	for _, s := range c.Statistics {
		out.Statistics[aws.ToString(s.Name)] = aws.ToString(s.Value)
	}
	for _, t := range c.Tags {
		out.Tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}

// Services lists the services of a cluster.
func (c *Client) Services(ctx context.Context, cluster string) ([]model.Service, error) {
	if cluster == "" {
		return nil, nil
	}
	var arns []string
	pager := ecs.NewListServicesPaginator(c.ECS, &ecs.ListServicesInput{
		Cluster:    aws.String(cluster),
		MaxResults: aws.Int32(100),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list services: %w", err)
		}
		arns = append(arns, page.ServiceArns...)
	}
	if len(arns) == 0 {
		return nil, nil
	}

	var (
		mu  sync.Mutex
		out []model.Service
	)
	err := forEachBatch(ctx, arns, describeServicesBatch, func(ctx context.Context, batch []string) error {
		res, err := c.ECS.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  aws.String(cluster),
			Services: batch,
			Include:  []ecstypes.ServiceField{ecstypes.ServiceFieldTags},
		})
		if err != nil {
			return fmt.Errorf("describe services: %w", err)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, s := range res.Services {
			out = append(out, toService(cluster, s))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	c.enrichServiceSizes(ctx, out)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// enrichServiceSizes fills in reserved CPU and memory from each service's task
// definition. Sizes are cached across refreshes, so this costs one describe per
// revision rather than one per refresh. Failures leave the columns blank.
func (c *Client) enrichServiceSizes(ctx context.Context, services []model.Service) {
	refs := map[string]bool{}
	for _, s := range services {
		if s.TaskDefinition != "" {
			refs[s.TaskDefinition] = true
		}
	}
	list := make([]string, 0, len(refs))
	for ref := range refs {
		list = append(list, ref)
	}

	var (
		mu    sync.Mutex
		sizes = map[string]model.TaskDef{}
	)
	_ = forEach(ctx, list, func(ctx context.Context, ref string) error {
		td, err := c.taskDefCached(ctx, ref)
		if err != nil {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		sizes[ref] = td
		return nil
	})

	for i := range services {
		if td, ok := sizes[services[i].TaskDefinition]; ok {
			services[i].CPU, services[i].Memory = td.CPU, td.Memory
		}
	}
}

// taskDefCached describes a task definition, memoising the result.
func (c *Client) taskDefCached(ctx context.Context, ref string) (model.TaskDef, error) {
	if v, ok := c.tdCache.Load(ref); ok {
		return v.(model.TaskDef), nil
	}
	td, err := c.TaskDef(ctx, ref)
	if err != nil {
		return model.TaskDef{}, err
	}
	c.tdCache.Store(ref, td)
	return td, nil
}

func toService(cluster string, s ecstypes.Service) model.Service {
	out := model.Service{
		Name:           aws.ToString(s.ServiceName),
		ARN:            aws.ToString(s.ServiceArn),
		Cluster:        cluster,
		Status:         aws.ToString(s.Status),
		LaunchType:     string(s.LaunchType),
		TaskDefinition: ShortARN(aws.ToString(s.TaskDefinition)),
		Scheduling:     string(s.SchedulingStrategy),
		Desired:        int(s.DesiredCount),
		Running:        int(s.RunningCount),
		Pending:        int(s.PendingCount),
		Deployments:    len(s.Deployments),
		PlatformVer:    aws.ToString(s.PlatformVersion),
		CreatedAt:      aws.ToTime(s.CreatedAt),
		Tags:           map[string]string{},
		Raw:            s,
	}
	if out.LaunchType == "" && len(s.CapacityProviderStrategy) > 0 {
		out.LaunchType = "CAPACITY_PROVIDER"
	}
	// The primary deployment carries the rollout state that tells you whether a
	// release is still in flight.
	for _, d := range s.Deployments {
		if aws.ToString(d.Status) != "PRIMARY" {
			continue
		}
		out.RolloutState = string(d.RolloutState)
		out.RolloutReason = aws.ToString(d.RolloutStateReason)
		out.UpdatedAt = aws.ToTime(d.UpdatedAt)
	}
	for _, e := range s.Events {
		out.Events = append(out.Events, model.Event{
			ID:        aws.ToString(e.Id),
			Service:   out.Name,
			Message:   aws.ToString(e.Message),
			CreatedAt: aws.ToTime(e.CreatedAt),
		})
	}
	for _, t := range s.Tags {
		out.Tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}

// TaskQuery narrows a task listing.
type TaskQuery struct {
	Cluster       string
	Service       string
	Family        string
	Instance      string
	DesiredStatus string // RUNNING, STOPPED or empty for both
}

// Tasks lists tasks in a cluster, optionally scoped to a service, family or
// container instance. An empty DesiredStatus lists running and stopped tasks,
// which is what the plain :tasks view shows.
func (c *Client) Tasks(ctx context.Context, q TaskQuery) ([]model.Task, error) {
	if q.Cluster == "" {
		return nil, nil
	}

	statuses := []ecstypes.DesiredStatus{ecstypes.DesiredStatusRunning, ecstypes.DesiredStatusStopped}
	if q.DesiredStatus != "" {
		statuses = []ecstypes.DesiredStatus{ecstypes.DesiredStatus(strings.ToUpper(q.DesiredStatus))}
	}

	var arns []string
	for _, status := range statuses {
		in := &ecs.ListTasksInput{
			Cluster:       aws.String(q.Cluster),
			DesiredStatus: status,
			MaxResults:    aws.Int32(100),
		}
		if q.Service != "" {
			in.ServiceName = aws.String(q.Service)
		}
		if q.Family != "" {
			in.Family = aws.String(q.Family)
		}
		if q.Instance != "" {
			in.ContainerInstance = aws.String(q.Instance)
		}
		pager := ecs.NewListTasksPaginator(c.ECS, in)
		for pager.HasMorePages() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("list tasks: %w", err)
			}
			arns = append(arns, page.TaskArns...)
		}
	}
	if len(arns) == 0 {
		return nil, nil
	}

	var (
		mu  sync.Mutex
		out []model.Task
	)
	err := forEachBatch(ctx, arns, describeTasksBatch, func(ctx context.Context, batch []string) error {
		res, err := c.ECS.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(q.Cluster),
			Tasks:   batch,
			Include: []ecstypes.TaskField{ecstypes.TaskFieldTags},
		})
		if err != nil {
			return fmt.Errorf("describe tasks: %w", err)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, t := range res.Tasks {
			out = append(out, toTask(q.Cluster, t))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Newest first: when something has just gone wrong, the interesting task is
	// almost always the most recent one.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func toTask(cluster string, t ecstypes.Task) model.Task {
	def := ShortARN(aws.ToString(t.TaskDefinitionArn))
	family, revision := splitTaskDef(def)

	out := model.Task{
		ID:             ShortARN(aws.ToString(t.TaskArn)),
		ARN:            aws.ToString(t.TaskArn),
		Cluster:        cluster,
		Group:          aws.ToString(t.Group),
		TaskDefinition: def,
		Family:         family,
		Revision:       revision,
		LastStatus:     aws.ToString(t.LastStatus),
		DesiredStatus:  aws.ToString(t.DesiredStatus),
		HealthStatus:   string(t.HealthStatus),
		LaunchType:     string(t.LaunchType),
		CapacityProv:   aws.ToString(t.CapacityProviderName),
		PlatformVer:    aws.ToString(t.PlatformVersion),
		AZ:             aws.ToString(t.AvailabilityZone),
		ContainerInst:  ShortARN(aws.ToString(t.ContainerInstanceArn)),
		CPU:            aws.ToString(t.Cpu),
		Memory:         aws.ToString(t.Memory),
		StoppedReason:  aws.ToString(t.StoppedReason),
		StopCode:       string(t.StopCode),
		ExecEnabled:    t.EnableExecuteCommand,
		CreatedAt:      aws.ToTime(t.CreatedAt),
		StartedAt:      aws.ToTime(t.StartedAt),
		StoppedAt:      aws.ToTime(t.StoppedAt),
		Tags:           map[string]string{},
		Raw:            t,
	}
	// "service:my-svc" is how ECS records service ownership on a task.
	if strings.HasPrefix(out.Group, "service:") {
		out.Service = strings.TrimPrefix(out.Group, "service:")
	}
	out.ConnectivityIP = taskIP(t)
	for _, ct := range t.Containers {
		out.Containers = append(out.Containers, toContainer(cluster, out.ID, out.ARN, ct))
	}
	for _, tag := range t.Tags {
		out.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return out
}

// taskIP prefers the awsvpc private address and falls back to whatever the
// container reports for bridge/host networking.
func taskIP(t ecstypes.Task) string {
	for _, a := range t.Attachments {
		if !strings.EqualFold(aws.ToString(a.Type), "ElasticNetworkInterface") {
			continue
		}
		for _, d := range a.Details {
			if aws.ToString(d.Name) == "privateIPv4Address" {
				return aws.ToString(d.Value)
			}
		}
	}
	for _, ct := range t.Containers {
		for _, ni := range ct.NetworkInterfaces {
			if ip := aws.ToString(ni.PrivateIpv4Address); ip != "" {
				return ip
			}
		}
	}
	return ""
}

func toContainer(cluster, taskID, taskARN string, ct ecstypes.Container) model.Container {
	out := model.Container{
		Name:         aws.ToString(ct.Name),
		TaskID:       taskID,
		TaskARN:      taskARN,
		Cluster:      cluster,
		Image:        aws.ToString(ct.Image),
		ImageDigest:  aws.ToString(ct.ImageDigest),
		LastStatus:   aws.ToString(ct.LastStatus),
		HealthStatus: string(ct.HealthStatus),
		ARN:          aws.ToString(ct.ContainerArn),
		RuntimeID:    aws.ToString(ct.RuntimeId),
		CPU:          aws.ToString(ct.Cpu),
		Memory:       aws.ToString(ct.Memory),
		MemoryRes:    aws.ToString(ct.MemoryReservation),
		ExitCode:     ct.ExitCode,
		Reason:       aws.ToString(ct.Reason),
		Raw:          ct,
	}
	for _, b := range ct.NetworkBindings {
		out.Ports = append(out.Ports, fmt.Sprintf("%d:%d/%s",
			aws.ToInt32(b.HostPort), aws.ToInt32(b.ContainerPort), string(b.Protocol)))
	}
	for _, ni := range ct.NetworkInterfaces {
		if ip := aws.ToString(ni.PrivateIpv4Address); ip != "" {
			out.Networks = append(out.Networks, ip)
		}
	}
	return out
}

func splitTaskDef(def string) (family, revision string) {
	if i := strings.LastIndex(def, ":"); i > 0 {
		return def[:i], def[i+1:]
	}
	return def, ""
}

// TaskDefFamilies lists task definition families in the region.
func (c *Client) TaskDefFamilies(ctx context.Context, prefix string) ([]string, error) {
	in := &ecs.ListTaskDefinitionFamiliesInput{Status: ecstypes.TaskDefinitionFamilyStatusActive}
	if prefix != "" {
		in.FamilyPrefix = aws.String(prefix)
	}
	var out []string
	pager := ecs.NewListTaskDefinitionFamiliesPaginator(c.ECS, in)
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list task definition families: %w", err)
		}
		out = append(out, page.Families...)
	}
	sort.Strings(out)
	return out, nil
}

// TaskDefs lists revisions of a family, newest first. With no family it lists
// the latest active revision of every family, which keeps the default view
// useful on accounts with thousands of revisions.
func (c *Client) TaskDefs(ctx context.Context, family string) ([]model.TaskDef, error) {
	if family == "" {
		return c.latestTaskDefPerFamily(ctx)
	}

	var arns []string
	pager := ecs.NewListTaskDefinitionsPaginator(c.ECS, &ecs.ListTaskDefinitionsInput{
		FamilyPrefix: aws.String(family),
		Sort:         ecstypes.SortOrderDesc,
		MaxResults:   aws.Int32(100),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list task definitions: %w", err)
		}
		arns = append(arns, page.TaskDefinitionArns...)
	}
	return c.describeTaskDefs(ctx, arns)
}

func (c *Client) latestTaskDefPerFamily(ctx context.Context) ([]model.TaskDef, error) {
	families, err := c.TaskDefFamilies(ctx, "")
	if err != nil {
		return nil, err
	}
	var (
		mu   sync.Mutex
		arns []string
	)
	err = forEach(ctx, families, func(ctx context.Context, fam string) error {
		res, err := c.ECS.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{
			FamilyPrefix: aws.String(fam),
			Sort:         ecstypes.SortOrderDesc,
			MaxResults:   aws.Int32(1),
			Status:       ecstypes.TaskDefinitionStatusActive,
		})
		if err != nil {
			return fmt.Errorf("list task definitions for %s: %w", fam, err)
		}
		mu.Lock()
		defer mu.Unlock()
		arns = append(arns, res.TaskDefinitionArns...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c.describeTaskDefs(ctx, arns)
}

func (c *Client) describeTaskDefs(ctx context.Context, arns []string) ([]model.TaskDef, error) {
	var (
		mu  sync.Mutex
		out []model.TaskDef
	)
	err := forEach(ctx, arns, func(ctx context.Context, arn string) error {
		td, err := c.TaskDef(ctx, arn)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		out = append(out, td)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family == out[j].Family {
			return out[i].Revision > out[j].Revision
		}
		return out[i].Family < out[j].Family
	})
	return out, nil
}

// TaskDef describes a single task definition by ARN or family:revision.
func (c *Client) TaskDef(ctx context.Context, ref string) (model.TaskDef, error) {
	res, err := c.ECS.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(ref),
	})
	if err != nil {
		return model.TaskDef{}, fmt.Errorf("describe task definition %s: %w", ref, err)
	}
	td := res.TaskDefinition
	out := model.TaskDef{
		Family:       aws.ToString(td.Family),
		Revision:     int(td.Revision),
		ARN:          aws.ToString(td.TaskDefinitionArn),
		Status:       string(td.Status),
		CPU:          aws.ToString(td.Cpu),
		Memory:       aws.ToString(td.Memory),
		NetworkMode:  string(td.NetworkMode),
		TaskRole:     ShortARN(aws.ToString(td.TaskRoleArn)),
		ExecRole:     ShortARN(aws.ToString(td.ExecutionRoleArn)),
		RegisteredAt: aws.ToTime(td.RegisteredAt),
		RegisteredBy: aws.ToString(td.RegisteredBy),
		Raw:          td,
	}
	for _, r := range td.RequiresCompatibilities {
		out.Requires = append(out.Requires, string(r))
	}
	for _, cd := range td.ContainerDefinitions {
		out.Containers = append(out.Containers, aws.ToString(cd.Name))
	}
	return out, nil
}

// LogConfig is the awslogs configuration of one container definition.
type LogConfig struct {
	Group        string
	StreamPrefix string
	Region       string
	Driver       string
}

// ContainerLogConfigs maps container name to log configuration for a task
// definition. Containers on a driver other than awslogs are still returned so
// the log view can explain why it has nothing to show.
func (c *Client) ContainerLogConfigs(ctx context.Context, taskDef string) (map[string]LogConfig, error) {
	res, err := c.ECS.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(taskDef),
	})
	if err != nil {
		return nil, fmt.Errorf("describe task definition %s: %w", taskDef, err)
	}
	out := map[string]LogConfig{}
	for _, cd := range res.TaskDefinition.ContainerDefinitions {
		name := aws.ToString(cd.Name)
		if cd.LogConfiguration == nil {
			out[name] = LogConfig{}
			continue
		}
		lc := LogConfig{Driver: string(cd.LogConfiguration.LogDriver)}
		opts := cd.LogConfiguration.Options
		lc.Group = opts["awslogs-group"]
		lc.StreamPrefix = opts["awslogs-stream-prefix"]
		lc.Region = opts["awslogs-region"]
		out[name] = lc
	}
	return out, nil
}

// Instances lists the container instances of a cluster, enriched with EC2
// instance types when ec2:DescribeInstances is permitted.
func (c *Client) Instances(ctx context.Context, cluster string) ([]model.Instance, error) {
	if cluster == "" {
		return nil, nil
	}
	var arns []string
	pager := ecs.NewListContainerInstancesPaginator(c.ECS, &ecs.ListContainerInstancesInput{
		Cluster:    aws.String(cluster),
		MaxResults: aws.Int32(100),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list container instances: %w", err)
		}
		arns = append(arns, page.ContainerInstanceArns...)
	}
	if len(arns) == 0 {
		return nil, nil
	}

	var (
		mu  sync.Mutex
		out []model.Instance
	)
	err := forEachBatch(ctx, arns, describeInstBatch, func(ctx context.Context, batch []string) error {
		res, err := c.ECS.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(cluster),
			ContainerInstances: batch,
		})
		if err != nil {
			return fmt.Errorf("describe container instances: %w", err)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, ci := range res.ContainerInstances {
			out = append(out, toInstance(cluster, ci))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	c.enrichInstanceTypes(ctx, out)
	sort.Slice(out, func(i, j int) bool { return out[i].EC2ID < out[j].EC2ID })
	return out, nil
}

func toInstance(cluster string, ci ecstypes.ContainerInstance) model.Instance {
	out := model.Instance{
		ID:             ShortARN(aws.ToString(ci.ContainerInstanceArn)),
		ARN:            aws.ToString(ci.ContainerInstanceArn),
		EC2ID:          aws.ToString(ci.Ec2InstanceId),
		Cluster:        cluster,
		Status:         aws.ToString(ci.Status),
		AgentConnected: ci.AgentConnected,
		AgentVersion:   aws.ToString(ci.VersionInfo.AgentVersion),
		DockerVersion:  aws.ToString(ci.VersionInfo.DockerVersion),
		RunningTasks:   int(ci.RunningTasksCount),
		PendingTasks:   int(ci.PendingTasksCount),
		CapacityProv:   aws.ToString(ci.CapacityProviderName),
		RegisteredAt:   aws.ToTime(ci.RegisteredAt),
		Raw:            ci,
	}
	if ci.VersionInfo == nil {
		out.AgentVersion, out.DockerVersion = "", ""
	}
	for _, r := range ci.RegisteredResources {
		switch aws.ToString(r.Name) {
		case "CPU":
			out.CPURegistered = int(r.IntegerValue)
		case "MEMORY":
			out.MemRegistered = int(r.IntegerValue)
		}
	}
	for _, r := range ci.RemainingResources {
		switch aws.ToString(r.Name) {
		case "CPU":
			out.CPURemaining = int(r.IntegerValue)
		case "MEMORY":
			out.MemRemaining = int(r.IntegerValue)
		}
	}
	for _, a := range ci.Attributes {
		switch aws.ToString(a.Name) {
		case "ecs.availability-zone":
			out.AZ = aws.ToString(a.Value)
		case "ecs.instance-type":
			out.InstanceType = aws.ToString(a.Value)
		}
	}
	return out
}

// enrichInstanceTypes fills in instance types the ECS attributes did not carry.
// Missing ec2:DescribeInstances permission is not worth failing a refresh over,
// so errors leave the column blank.
func (c *Client) enrichInstanceTypes(ctx context.Context, instances []model.Instance) {
	var ids []string
	for _, i := range instances {
		if i.InstanceType == "" && i.EC2ID != "" {
			ids = append(ids, i.EC2ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	types, err := c.instanceTypes(ctx, ids)
	if err != nil {
		return
	}
	for i := range instances {
		if t, ok := types[instances[i].EC2ID]; ok {
			instances[i].InstanceType = t
		}
	}
}

// Events returns the recent events of every service in a cluster, newest first.
func (c *Client) Events(ctx context.Context, cluster, service string) ([]model.Event, error) {
	services, err := c.Services(ctx, cluster)
	if err != nil {
		return nil, err
	}
	var out []model.Event
	for _, s := range services {
		if service != "" && s.Name != service {
			continue
		}
		out = append(out, s.Events...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Deployments returns the in-flight deployments of a service.
func (c *Client) Deployments(ctx context.Context, cluster, service string) ([]model.Deployment, error) {
	res, err := c.ECS.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: []string{service},
	})
	if err != nil {
		return nil, fmt.Errorf("describe service %s: %w", service, err)
	}
	var out []model.Deployment
	for _, s := range res.Services {
		for _, d := range s.Deployments {
			out = append(out, model.Deployment{
				ID:             aws.ToString(d.Id),
				Service:        aws.ToString(s.ServiceName),
				Status:         aws.ToString(d.Status),
				TaskDefinition: ShortARN(aws.ToString(d.TaskDefinition)),
				Desired:        int(d.DesiredCount),
				Running:        int(d.RunningCount),
				Pending:        int(d.PendingCount),
				Failed:         int(d.FailedTasks),
				RolloutState:   string(d.RolloutState),
				RolloutReason:  aws.ToString(d.RolloutStateReason),
				LaunchType:     string(d.LaunchType),
				CreatedAt:      aws.ToTime(d.CreatedAt),
				UpdatedAt:      aws.ToTime(d.UpdatedAt),
				Raw:            d,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// CapacityProviders lists the capacity providers attached to a cluster, or all
// of them in the region when cluster is empty.
func (c *Client) CapacityProviders(ctx context.Context, cluster string) ([]model.CapacityProvider, error) {
	var names []string
	if cluster != "" {
		res, err := c.ECS.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{cluster}})
		if err != nil {
			return nil, fmt.Errorf("describe cluster %s: %w", cluster, err)
		}
		for _, cl := range res.Clusters {
			names = append(names, cl.CapacityProviders...)
		}
		if len(names) == 0 {
			return nil, nil
		}
	}

	in := &ecs.DescribeCapacityProvidersInput{Include: []ecstypes.CapacityProviderField{ecstypes.CapacityProviderFieldTags}}
	if len(names) > 0 {
		in.CapacityProviders = names
	}
	var out []model.CapacityProvider
	for {
		res, err := c.ECS.DescribeCapacityProviders(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("describe capacity providers: %w", err)
		}
		for _, cp := range res.CapacityProviders {
			out = append(out, toCapacityProvider(cp))
		}
		if res.NextToken == nil {
			break
		}
		in.NextToken = res.NextToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func toCapacityProvider(cp ecstypes.CapacityProvider) model.CapacityProvider {
	out := model.CapacityProvider{
		Name:         aws.ToString(cp.Name),
		ARN:          aws.ToString(cp.CapacityProviderArn),
		Status:       string(cp.Status),
		UpdateStatus: string(cp.UpdateStatus),
		Tags:         map[string]string{},
		Raw:          cp,
	}
	if asg := cp.AutoScalingGroupProvider; asg != nil {
		out.AutoScalingGroup = ShortARN(aws.ToString(asg.AutoScalingGroupArn))
		out.ManagedTermination = string(asg.ManagedTerminationProtection)
		if ms := asg.ManagedScaling; ms != nil {
			out.ManagedScaling = string(ms.Status)
			out.TargetCapacity = int(aws.ToInt32(ms.TargetCapacity))
		}
	}
	for _, t := range cp.Tags {
		out.Tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}

// Scale sets the desired count of a service.
func (c *Client) Scale(ctx context.Context, cluster, service string, desired int32) error {
	_, err := c.ECS.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(cluster),
		Service:      aws.String(service),
		DesiredCount: aws.Int32(desired),
	})
	if err != nil {
		return fmt.Errorf("scale %s to %d: %w", service, desired, err)
	}
	return nil
}

// Restart forces a new deployment of a service, which is the ECS equivalent of
// a rollout restart.
func (c *Client) Restart(ctx context.Context, cluster, service string) error {
	_, err := c.ECS.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:            aws.String(cluster),
		Service:            aws.String(service),
		ForceNewDeployment: true,
	})
	if err != nil {
		return fmt.Errorf("restart %s: %w", service, err)
	}
	return nil
}

// SetTaskDef points a service at a different task definition revision.
func (c *Client) SetTaskDef(ctx context.Context, cluster, service, taskDef string) error {
	_, err := c.ECS.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:        aws.String(cluster),
		Service:        aws.String(service),
		TaskDefinition: aws.String(taskDef),
	})
	if err != nil {
		return fmt.Errorf("set %s task definition to %s: %w", service, taskDef, err)
	}
	return nil
}

// StopTask stops a task. A service-owned task is replaced by the scheduler,
// which makes this the ECS analogue of deleting a pod.
func (c *Client) StopTask(ctx context.Context, cluster, task, reason string) error {
	if reason == "" {
		reason = "Stopped via ecis"
	}
	_, err := c.ECS.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(cluster),
		Task:    aws.String(task),
		Reason:  aws.String(reason),
	})
	if err != nil {
		return fmt.Errorf("stop task %s: %w", ShortARN(task), err)
	}
	return nil
}

// forEachBatch splits ids into chunks of size n and runs fn over them with
// bounded concurrency, returning the first error encountered.
func forEachBatch(ctx context.Context, ids []string, n int, fn func(context.Context, []string) error) error {
	var batches [][]string
	for i := 0; i < len(ids); i += n {
		end := i + n
		if end > len(ids) {
			end = len(ids)
		}
		batches = append(batches, ids[i:end])
	}
	return runBounded(ctx, len(batches), func(ctx context.Context, i int) error {
		return fn(ctx, batches[i])
	})
}

// forEach runs fn over every id with bounded concurrency.
func forEach(ctx context.Context, ids []string, fn func(context.Context, string) error) error {
	return runBounded(ctx, len(ids), func(ctx context.Context, i int) error {
		return fn(ctx, ids[i])
	})
}

func runBounded(ctx context.Context, n int, fn func(context.Context, int) error) error {
	if n == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, maxParallelDescribes)
		errOnce  sync.Once
		firstErr error
	)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			// An earlier worker failed; stop scheduling more work.
			i = n
			continue
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}(i)
	}
	wg.Wait()
	return firstErr
}

// Duration renders a duration the way k9s renders age: the two most significant
// units, never more than five characters wide.
func Duration(d time.Duration) string {
	if d <= 0 {
		return "n/a"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%02ds", m, s)
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%02dm", h, m)
	case d < 365*24*time.Hour:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) % 24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%02dh", days, h)
	default:
		years := int(d.Hours()) / 24 / 365
		days := (int(d.Hours()) / 24) % 365
		return fmt.Sprintf("%dy%dd", years, days)
	}
}

// Since renders the age of a timestamp.
func Since(t time.Time) string {
	if t.IsZero() {
		return "n/a"
	}
	return Duration(time.Since(t))
}
