# Description
```
单个账户, 仅QOS
```
# Input
```
$ qosd add-genesis-accounts address1ctmavdk57x0q7c9t98v7u79607222ars4qczcy,10000QOS
```
# Output
```
$ qosd add-genesis-accounts address1ctmavdk57x0q7c9t98v7u79607222ars4qczcy,10000QOS

```
命令行无返回值, `genesis.json`文件中`app-state`中`accounts`部分新增:
```
      {
        "base_account": {
          "account_address": "address1ctmavdk57x0q7c9t98v7u79607222ars4qczcy",
          "public_key": null,
          "nonce": "0"
        },
        "qos": "10000",
        "qscs": []
      }
```