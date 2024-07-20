# GitLab Artifactory Deployer Go

GitLab Artifactory Deployer written in Go.

- Rerieve job id from `deployable_id` key in request body
- Retrieve the status from `status` key in request body
- If status is `success` wait 3 seconds to let the zip process finish and then download the zip file
- .. Download the zip file from GitLab ... TODO
- Craete logging on routes, exclude `favicon.ico`

## Development

Run: `gow run .`

### Setup GitLab Artifact Deployer

#### Environment variables options

You need to set some settings using environment variables, for that we use the `.env` file. You can use the [.env.example](.env.example) file as template:

```sh
cp .env.example .env
```

See below for all the available options, only the `GITLAB_SECRET_TOKEN` environment variable is actually mandatory:

| Environment Var           | Description                                                                                        | Required |
| ------------------------- | -------------------------------------------------------------------------------------------------- | -------- |
| `GITLAB_SECRET_TOKEN`     | GitLab Secret Token, which is **required** for safety reasons.                                     | yes      |
| `GITLAB_HOSTNAME`         | GitLab Host, default: `gitlab.com`                                                                 | no       |
| `USE_JOB_NAME`            | Instead of Job ID from the webhook body request, use job name and branch name (not set by default) | no       |
| `PROJECT_ID`              | GitLab Project ID (not set by default), retrieving project ID from webhook body request            | no       |
| `REPO_BRANCH`             | Branch to download artifact from, default: `main`                                                  | no       |
| `JOB_NAME`                | Job name to download artifact from, default: `deploy`                                              | no       |
| `ACCESS_TOKEN`            | Access token, for private repository (not set by default)                                          | no       |
| `DESTINATION_PATH`        | Destination path where the artifact zip content is extracted, default: `dest` folder               | no       |
| `TEMP_FOLDER`             | Temporarily file path where the artifact zip is stored, default: `tmp` folder                      | no       |
| `POST_DEPLOYMENT_COMMAND` | Optional post-deployment command in the `POST_DEPLOYMENT_CWD`. Eg. `php spark cache:clear`         | no       |
| `POST_DEPLOYMENT_CWD`     | Set the current working directory for the post-deployment command, default: `$DESTINATION_PATH`    | no       |
