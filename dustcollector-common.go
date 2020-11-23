package dustcollector

import (
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/sts"
)

func containsStringPointer(strSlice []*string, searchStr *string) bool {
    for _, value := range strSlice {
        if *value == *searchStr {
            return true
        }
    }
    return false
}

func containsString(strSlice []string, searchStr string) bool {
    for _, value := range strSlice {
        if value == searchStr {
            return true
        }
    }
    return false
}

func dedupeStringPointer(strSlice []*string) []*string {
    var returnSlice []*string
    for _, value := range strSlice {
        if !containsStringPointer(returnSlice, value) {
            returnSlice = append(returnSlice, value)
        }
    }
    return returnSlice
}

func dedupeString(strSlice []string) []string {
    var returnSlice []string
    for _, value := range strSlice {
        if !containsString(returnSlice, value) {
            returnSlice = append(returnSlice, value)
        }
    }
    return returnSlice
}

// describeImagesOwnedByThisAccount takes a given session and pull all images (AMI's)
// for that account and returns them as a slice of Image object along with any errors.
// It first checks the current account context and only pulls images that are owned
// by the current account.
func describeImagesOwnedByThisAccount(sess *session.Session) (images []*ec2.Image, err error) {
	svcSts := sts.New(sess)
	gcii := sts.GetCallerIdentityInput{}
	gci, err := svcSts.GetCallerIdentity(&gcii)
	if err != nil {
		return images, err
	}
	var accounts []*string
	accounts = append(accounts, gci.Account)
	svc := ec2.New(sess)
	input := ec2.DescribeImagesInput{
		Owners: accounts,
	}
	// apparently there's no paginator for getting images
	results, err := svc.DescribeImages(&input)
	if err != nil {
		return images, err
	}
	images = results.Images
	return images, err
}

// describeASGs takes a given session and returns a slice of all AutoScaling Groups
// found in the account along with any errors. It handles pagination.
func describeASGs(sess *session.Session) (asgs []*autoscaling.Group, err error) {
	svc := autoscaling.New(sess)
	input := autoscaling.DescribeAutoScalingGroupsInput{}
	results, err := svc.DescribeAutoScalingGroups(&input)
	if err != nil {
		return asgs, err
	}
	asgs = results.AutoScalingGroups
	i := 2
	max := 50
	for i < max {
		if results.NextToken != nil {
			input = autoscaling.DescribeAutoScalingGroupsInput{
				NextToken: results.NextToken,
			}
			results, err = svc.DescribeAutoScalingGroups(&input)
			if err != nil {
				return asgs, err
			}
			asgs = append(asgs, results.AutoScalingGroups...)
		} else {
			break
		}
		i += 1
	}
	return asgs, err
}

// lcInASGS takes a launch configuration name and a list of autoscaling groups and searches
// the ASGs for any references to that launch configuration. It returns the result as a slice
// of ASG name strings.
func lcInASGs(lcName string, allAsgs []*autoscaling.Group) (inASGs bool, asgNames []string) {
	for _, asg := range allAsgs {
		asgLcName := asg.LaunchConfigurationName
		if asgLcName != nil {
			if *asgLcName == lcName {
				inASGs = true
				asgNames = append(asgNames, *asg.AutoScalingGroupName)
			}
		}
	}
	return inASGs, asgNames
}

// ltInASGS takes a launch template name and a list of autoscaling groups and searches
// the ASGs for any references to that launch template. It returns the result as a slice
// of ASG name strings.
func ltInASGs(ltName string, allAsgs []*autoscaling.Group) (inASGs bool, asgNames []string) {
	for _, asg := range allAsgs {
		if asg.LaunchTemplate != nil {
			asgLtName := asg.LaunchTemplate.LaunchTemplateName
			if asgLtName != nil {
				if *asgLtName == ltName {
					inASGs = true
					asgNames = append(asgNames, *asg.AutoScalingGroupName)
				}
			}
		}
	}
	return inASGs, asgNames
}

// ltsWithSnapImage takes a slice of LaunchTemplateVersion, a snapshotId, and an imageId and
// returns a slice with the names of any launch templates that contain a reference
// to the given snapshot or image ID. This is useful for finding out if a given snapshot
// or image is involved in any launch templates before the snapshot or image is modified
// or deleted.
func ltsWithSnapImage(lts []*ec2.LaunchTemplateVersion, snapshotId, imageId string) (ltNames []string) {
	for _, lt := range lts {
		if *lt.LaunchTemplateData.ImageId == imageId {
			ltNames = append(ltNames, *lt.LaunchTemplateName)
		}
		for _, bdm := range lt.LaunchTemplateData.BlockDeviceMappings {
			bdmSnap := bdm.Ebs.SnapshotId
			if bdmSnap != nil {
				if *bdmSnap == snapshotId {
					ltNames = append(ltNames, *lt.LaunchTemplateName)
				}
			}
		}
	}
	return ltNames
}

// lcsWithSnapImage takes a slice of LaunchConfiguration, a snapshotId, and an imageId and
// returns a slice with the names of any launch configuration that contains a reference
// to the given snapshot or image ID. This is useful for finding out if a given snapshot
// or image is involved in any launch configurations before the snapshot or image is modified
// or deleted.
func lcsWithSnapImage(lcs []*autoscaling.LaunchConfiguration, snapshotId, imageId string) (lcNames []string) {
	for _, lc := range lcs {
		if *lc.ImageId == imageId {
			lcNames = append(lcNames, *lc.LaunchConfigurationName)
		}
		for _, bdm := range lc.BlockDeviceMappings {
			if bdm.Ebs != nil {
				bdmSnap := bdm.Ebs.SnapshotId
				if bdmSnap != nil {
					if *bdmSnap == snapshotId {
						lcNames = append(lcNames, *lc.LaunchConfigurationName)
					}
				}
			}
		}
	}
	return lcNames
}

// makeBatchesStringPointer takes a slice of string pointers and returns them as
// a slice of string pointer slices in batch size of batchSize. Useful for splitting
// up work into batches for parallel operations.
func makeBatchesStringPointer(strSlice []*string, batchSize int) (batches [][]*string) {
    numBatches, remainder := len(strSlice)/batchSize, len(strSlice)%batchSize
    // build full batches
    for i := 1; i <= numBatches; i++ {
        var startIndex int
        endIndex := i * batchSize
        if i == 1 {
            startIndex = 0
        } else {
            startIndex = batchSize * (i - 1)
        }
        var b []*string
        b = strSlice[startIndex:endIndex]
        batches = append(batches, b)
    }
    if remainder > 0 {
        // build last partial batch
        startIndex := (len(strSlice) - remainder)
        endIndex := len(strSlice)
        var b []*string
        b = strSlice[startIndex:endIndex]
        batches = append(batches, b)
    }
    return batches
}
