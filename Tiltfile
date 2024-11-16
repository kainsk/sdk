allow_k8s_contexts('colima')

docker_compose("tests/basic/docker-compose.yaml")

dc_resource('nats', labels=['statefun'])
dc_resource('io', labels=['statefun'])
dc_resource('nats-exporter', labels=['statefun'])
dc_resource('runtime', labels=['runtime'])
dc_resource('prometheus', labels=['dashboard'])
dc_resource('grafana', labels=['dashboard'])
