#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

#include "solution.c"
#include "c_typed.h"

#define CF_ENTRY __ENTRY__

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

    int status = CF_RUN(CF_ENTRY, input);

    free(input);
    return status;
}
