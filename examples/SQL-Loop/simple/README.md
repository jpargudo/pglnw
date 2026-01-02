# Most simple example

This example is the simpliest one:

Prerequisites

 - PostgreSQL Server, any version shoud work
 - user has LOGIN capabilities

No creation of tables and others are needed, so there's no need to call for
`-create <create.json>` or such. pglnw accepts the omission of parameter
`-create`.

`script.sql` contains a simple "select 1;". There's also a comment starting
with `--`, like in ... plain SQL. So that means this file is just the SQL
script you want it to be. Simple as that.


Once pglnw has been compiled **and** the `config.json` adapted to suit
your needs, this could be used as simple as:

```code
$ pglnw -config config.json -script script.sql
```

You can throtle it down asking pglnw to wait for 1 second and a half like
this:

```code
$ pglnw -config config.json -script script.sql -sleep 1s500ms
```

If you want to limit the number of loops, you can do that as simply as 

```code
$ pglnw -config config.json -script script.sql -loops 10
```

Alternatively, you can limit the execution time, setting a duration:

```code
$ pglnw -config config.json -script script.sql -time 5s
```
You can do both at the same time. Whichever happens first will
break the SQL-Loop:

```code
$ pglnw -config config.json -script script.sql -time 1s -loops 20
$ pglnw -config config.json -script script.sql -time 10s -loops 20
```
