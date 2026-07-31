package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// describeInstancesBatch is the number of instance ids per DescribeInstances
// call. The API has no hard cap, but batching keeps the request URL sane.
const describeInstancesBatch = 100

// instanceTypes maps EC2 instance ids to their instance type.
func (c *Client) instanceTypes(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for i := 0; i < len(ids); i += describeInstancesBatch {
		end := i + describeInstancesBatch
		if end > len(ids) {
			end = len(ids)
		}
		res, err := c.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			InstanceIds: ids[i:end],
		})
		if err != nil {
			return nil, fmt.Errorf("describe ec2 instances: %w", err)
		}
		for _, r := range res.Reservations {
			for _, inst := range r.Instances {
				out[aws.ToString(inst.InstanceId)] = string(inst.InstanceType)
			}
		}
	}
	return out, nil
}
