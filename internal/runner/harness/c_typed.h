#ifndef CF_JUDGE_C_TYPED_H
#define CF_JUDGE_C_TYPED_H

#include <ctype.h>
#include <errno.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>

static int cf_judge_read_int(const char **cursor, int *value)
{
    char *end = NULL;
    errno = 0;

    long number = strtol(*cursor, &end, 10);

    if (end == *cursor || errno == ERANGE ||
        number < INT_MIN || number > INT_MAX)
    {
        fputs("invalid or out-of-range integer argument\n", stderr);
        return 1;
    }

    *cursor = end;
    *value = (int)number;
    return 0;
}

static int cf_judge_end_of_args(const char *cursor)
{
    while (isspace((unsigned char)*cursor))
    {
        cursor++;
    }

    if (*cursor != '\0')
    {
        fputs("unexpected input after last argument\n", stderr);
        return 1;
    }

    return 0;
}

// int Solve(int value)
static int cf_judge_run_int1(
    int (*function)(int),
    const char *input)
{
    int value;

    if (cf_judge_read_int(&input, &value) ||
        cf_judge_end_of_args(input))
    {
        return 1;
    }

    return printf("%d", function(value)) < 0 ? 1 : 0;
}

// int Solve(int a, int b)
static int cf_judge_run_int2(
    int (*function)(int, int),
    const char *input)
{
    int first, second;

    if (cf_judge_read_int(&input, &first) ||
        cf_judge_read_int(&input, &second) ||
        cf_judge_end_of_args(input))
    {
        return 1;
    }

    return printf("%d", function(first, second)) < 0 ? 1 : 0;
}

static int cf_judge_run_string(
    const char *(*function)(const char *),
    const char *input)
{
    const char *result = function(input);

    if (result == NULL)
    {
        fputs("Solve returned NULL\n", stderr);
        return 1;
    }

    return fputs(result, stdout) == EOF ? 1 : 0;
}

#define CF_RUN(entry, input) _Generic(&(entry),         \
    const char *(*)(const char *): cf_judge_run_string, \
    int (*)(int): cf_judge_run_int1,                    \
    int (*)(int, int): cf_judge_run_int2)(&(entry), (input))

#endif
