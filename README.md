# ICON RPC Load Balancer
The ICON RPC Load Balancer is a rpc load balancer that distributes incoming requests to all healthy Nodes on the ICON Blockchain.

There is a live version of the load balancer running at https://rpc.onyxgrid.com/api/v3. An overview of active RPC endpoints can be found at [onyxgrid.com/icon-rpcs](https://onyxgrid.com/icon-rpcs).

# Run your own instance
You can easily run your own instance of the ICON RPC Load Balancer by following the steps below.

1. Make sure you have a domain name, point the DNS to the server where you will be running the load balancer.
2. Clone the repository
3. Make an `.env` file in the root directory of the project and add the following environment variables:
```DOMAIN=yourdomain.com```
4. Run `docker-compose up -d` to start the load balancer

# Contributing
If you would like to contribute to the project, please fork the repository and submit a pull request. We are always looking for ways to improve the load balancer and would love to hear your ideas.
