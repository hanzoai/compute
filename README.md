<p align="center"><img src=".github/hero.svg" alt="compute" width="880"></p>

<h1 align="center" style="border-bottom: none;">Hanzo Compute</h1>
<h3 align="center">Cloud operating-system management platform (Go + React).</h3>

## Architecture

Hanzo Compute has two parts:

| Name     | Description                          | Language               | Source code                                |
|----------|--------------------------------------|------------------------|--------------------------------------------|
| Frontend | Web UI                               | JavaScript + React     | https://github.com/hanzoai/compute/tree/main/web |
| Backend  | RESTful API + Beego                  | Go + Beego + Postgres  | https://github.com/hanzoai/compute           |

## Installation

Hanzo Compute uses Hanzo IAM as the authentication system. Create an organization and an application for Compute in your IAM instance, then wire it via `app.conf`.

### Necessary configuration

#### Get the code

```shell
git clone https://github.com/hanzoai/iam
git clone https://github.com/hanzoai/compute
```

#### Setup database

Compute stores users, nodes, and resource information in a Postgres database named `hanzo_visor` (auto-created). The database keeps that name across the repo rename — renaming it is a migration, not a rename. The connection string is configured in `conf/app.conf`:

```ini
dataSourceName = postgres://user:pass@localhost:5432/hanzo_visor
```

#### Configure IAM

After creating an organization and an application in Hanzo IAM, update `clientID`, `clientSecret`, `iamOrganization`, and `iamApplication` in `app.conf`.

#### Run

```shell
go run main.go
```

Open browser: http://localhost:16001/

### Optional

#### RDP

Run guacd for RDP connections:

```shell
docker run --name guacd -d -p 4822:4822 guacamole/guacd
```

## License

[Apache-2.0](LICENSE)
