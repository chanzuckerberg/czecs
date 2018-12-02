# Change log

## 2018-12-17 v0.1.2
* Upgrade dependencies; adds support for ECS cluster/service/task definition tags

## 2018-10-04 v0.1.1
* Stricter parsing of JSON to reject unknown fields
* Add --timeout to upgrade/install/task for wait time for task completion/service stability

## 2018-09-29 v0.1.0

* [breaking] Remove template command
* Add register command, use --dry-run for previous template command functionality
* Add --task-definition-arn flag to upgrade and install

## 2018-09-11 v0.0.3

* Now uses Go 1.11 modules for dependency trakcing
* Upgrade vendored libraries
  * Upgrade to aws-sdk-go now supports private Docker repositories in ECS

## 2018-08-23 v0.0.2

* Bugfix: Don't swallow errors if czecs upgrade fails

## 2018-08-08 v0.0.1

* Initial release
