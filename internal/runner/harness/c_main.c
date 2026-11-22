#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

#include "solution.c"

#define CF_ENTRY __ENTRY__

_Static_assert(
    _Generic(&CF_ENTRY,
        const char *(*)(const char *): 1,
        default: 0),
    "Solve must have signature: const char *Solve(const char *)");

int main(void)
{
    size_t capacity = 4096;
    size_t length = 0;

    char *input = malloc(capacity);
    if (input == NULL)
    {
        fputs("failed to allocate input buffer\n", stderr);
        return 1;
    }

    int ch;

    while ((ch = fgetc(stdin)) != EOF)
    {
        if (length + 1 >= capacity)
        {
            if (capacity > SIZE_MAX / 2)
            {
                free(input);
                fputs("input too large\n", stderr);
                return 1;
            }

            size_t new_capacity = capacity * 2;
            char *resized = realloc(input, new_capacity);

            if (resized == NULL)
            {
                free(input);
                fputs("failed to resize input buffer\n", stderr);
                return 1;
            }

            input = resized;
            capacity = new_capacity;
        }

        input[length++] = (char)ch;
    }

    if (ferror(stdin))
    {
        free(input);
        fputs("failed to read stdin\n", stderr);
        return 1;
    }

    input[length] = '\0';

    const char *result = CF_ENTRY(input);

    if (result == NULL)
    {
        free(input);
        fputs("Solve returned NULL\n", stderr);
        return 1;
    }

    fputs(result, stdout);

    free(input);
    return 0;
}
